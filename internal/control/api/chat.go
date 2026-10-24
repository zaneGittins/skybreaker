package api

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zaneGittins/skybreaker/internal/control/app"
	"github.com/zaneGittins/skybreaker/internal/control/chat"
	"github.com/zaneGittins/skybreaker/internal/control/store"
	"github.com/zaneGittins/skybreaker/internal/notify/webpush"
	"github.com/zaneGittins/skybreaker/internal/webutil"
)

// chatEvents streams a chat's events: the stored ones after ?after= (or
// Last-Event-ID, when EventSource reconnects), then live ones. Stored
// events carry their seq as the SSE id; streamed text deltas carry none.
func (s *server) chatEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var after int64
	v := r.URL.Query().Get("after")
	if h := r.Header.Get("Last-Event-ID"); h != "" {
		v = h
	}
	if v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			webutil.Error(w, http.StatusBadRequest, "after must be a sequence number")
			return
		}
		after = n
	}
	// Subscribe before reading the log, so nothing falls between them.
	live, cancel := s.Backend.ChatSubscribe(id)
	defer cancel()
	past, err := s.Backend.ChatEvents(id, after)
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	var out io.Writer = w
	flush := func() error { return rc.Flush() }
	// A long conversation's replay compresses well; each event is flushed
	// through the compressor, so live events aren't held back.
	if webutil.AcceptsGzip(r) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		out = gz
		flush = func() error {
			if err := gz.Flush(); err != nil {
				return err
			}
			return rc.Flush()
		}
	}
	last := after
	send := func(e chat.Event) {
		b, err := json.Marshal(e)
		if err != nil {
			return
		}
		if e.Seq > 0 {
			fmt.Fprintf(out, "id: %d\n", e.Seq)
			last = e.Seq
		}
		fmt.Fprintf(out, "data: %s\n\n", b)
	}
	fmt.Fprint(out, "retry: 2000\n\n")
	for _, e := range past {
		send(e)
	}
	if flush() != nil {
		return
	}
	tick := time.NewTicker(25 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			fmt.Fprint(out, ": ping\n\n")
		case e, ok := <-live:
			if !ok {
				return // fell behind: the client reconnects from last
			}
			if e.Seq > 0 && e.Seq <= last {
				continue
			}
			send(e)
		}
		if flush() != nil {
			return
		}
	}
}

// maxSendBody fits a message's attachments, base64 in JSON, with their
// thumbnails, and its text with room for escaping.
const maxSendBody = chat.MaxAttachTotal/3*4 + 4<<20

func (s *server) chatSend(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text  string      `json:"text"`
		Files []chat.File `json:"files"`
	}
	if !s.decodeSend(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Text) == "" && len(in.Files) == 0 {
		webutil.Error(w, http.StatusBadRequest, "empty message")
		return
	}
	if err := s.Backend.ChatSend(r.Context(), r.PathValue("id"), in.Text, in.Files...); err != nil {
		chatFail(w, err)
		return
	}
	webutil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// decodeSend decodes a message. One with attachments takes several times
// its size in memory to decode, so only a few are decoded at once; the
// place is held for the decoding only, not for the sending after.
func (s *server) decodeSend(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.ContentLength >= 0 && r.ContentLength <= 1<<20 {
		return decodeUpTo(w, r, v, maxSendBody)
	}
	select {
	case s.uploads <- struct{}{}:
		defer func() { <-s.uploads }()
	case <-r.Context().Done():
		return false
	}
	return decodeUpTo(w, r, v, maxSendBody)
}

// chatFail maps a chat error: unknown sessions are 404, anything else the
// user can act on is 400.
func chatFail(w http.ResponseWriter, err error) {
	if errors.Is(err, app.ErrBusy) {
		fail(w, err)
		return
	}
	if errors.Is(err, chat.ErrTerminalLogin) {
		webutil.WriteJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "terminal": true})
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		webutil.Error(w, http.StatusNotFound, err.Error())
		return
	}
	if errors.Is(err, app.ErrNotFound) {
		fail(w, err)
		return
	}
	webutil.Error(w, http.StatusBadRequest, err.Error())
}

func (s *server) chatAnswer(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Ask string `json:"ask"`
		chat.Answer
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Ask == "" {
		webutil.Error(w, http.StatusBadRequest, "which ask?")
		return
	}
	if err := s.Backend.ChatAnswer(r.Context(), r.PathValue("id"), in.Ask, in.Answer); err != nil {
		chatFail(w, err)
		return
	}
	webutil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) chatInterrupt(w http.ResponseWriter, r *http.Request) {
	if err := s.Backend.ChatInterrupt(r.Context(), r.PathValue("id")); err != nil {
		chatFail(w, err)
		return
	}
	webutil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) chatMode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Mode string `json:"mode"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.Backend.ChatMode(r.Context(), r.PathValue("id"), in.Mode); err != nil {
		chatFail(w, err)
		return
	}
	webutil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) chatModel(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Model string `json:"model"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Model == "" {
		webutil.Error(w, http.StatusBadRequest, "which model?")
		return
	}
	sess, err := s.Backend.ChatModel(r.Context(), r.PathValue("id"), in.Model)
	if err != nil {
		chatFail(w, err)
		return
	}
	webutil.WriteJSON(w, http.StatusOK, map[string]any{"session": sess})
}

func (s *server) chatEffort(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Effort string `json:"effort"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Effort == "" {
		webutil.Error(w, http.StatusBadRequest, "which effort?")
		return
	}
	sess, err := s.Backend.ChatEffort(r.Context(), r.PathValue("id"), in.Effort)
	if err != nil {
		chatFail(w, err)
		return
	}
	webutil.WriteJSON(w, http.StatusOK, map[string]any{"session": sess})
}

func (s *server) chatLogin(w http.ResponseWriter, r *http.Request) {
	if err := s.Backend.ChatLogin(r.Context(), r.PathValue("id")); err != nil {
		chatFail(w, err)
		return
	}
	webutil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// chatTerminal opens (or returns) the terminal a chat continues in.
func (s *server) chatTerminal(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Minute)
	defer cancel()
	t, err := s.Backend.ChatTerminal(ctx, r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	webutil.WriteJSON(w, http.StatusOK, t)
}

// chatCompact has the harness summarise the conversation so far.
func (s *server) chatCompact(w http.ResponseWriter, r *http.Request) {
	if err := s.Backend.ChatCompact(r.Context(), r.PathValue("id")); err != nil {
		chatFail(w, err)
		return
	}
	webutil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// chatFork starts a chat that carries on from a copy of this one.
func (s *server) chatFork(w http.ResponseWriter, r *http.Request) {
	f, err := s.Backend.ChatFork(r.Context(), r.PathValue("id"))
	if err != nil {
		chatFail(w, err)
		return
	}
	webutil.WriteJSON(w, http.StatusOK, f)
}

func (s *server) seen(w http.ResponseWriter, r *http.Request) {
	if err := s.Backend.MarkSeen(r.PathValue("id")); err != nil {
		fail(w, err)
		return
	}
	webutil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) review(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("p")
	if !s.known(w, name) {
		return
	}
	rv, err := s.Backend.Review(r.Context(), name)
	if err != nil {
		webutil.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	webutil.WriteJSONGzip(w, r, http.StatusOK, rv)
}

// sessionReview is what a session has changed, in its own worktree or the
// project's workspace.
func (s *server) sessionReview(w http.ResponseWriter, r *http.Request) {
	rv, err := s.Backend.ReviewSession(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, app.ErrNotFound) {
			fail(w, err)
			return
		}
		webutil.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	webutil.WriteJSONGzip(w, r, http.StatusOK, rv)
}

// inboxSession is a session with its project's tint, for the inbox.
type inboxSession struct {
	store.Session
	Tint int `json:"tint"`
}

// inbox is the home screen: every project, every session that isn't a
// chat's own terminal, and the harness approvals waiting.
func (s *server) inbox(w http.ResponseWriter, r *http.Request) {
	names := slices.Sorted(maps.Keys(s.Config.Projects))
	views := make([]projectView, len(names))
	lists := make([][]store.Session, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Go(func() { views[i], lists[i] = s.view(r.Context(), name) })
	}
	wg.Wait()
	sessions := []inboxSession{}
	for _, list := range lists {
		for _, x := range list {
			if x.Parent == "" {
				sessions = append(sessions, inboxSession{Session: x, Tint: s.tints[x.Project]})
			}
		}
	}
	webutil.WriteJSONGzip(w, r, http.StatusOK, map[string]any{
		"projects": views,
		"sessions": sessions,
		"pending":  nonNil(s.Approvals.Pending("")),
		"requests": nonNil(s.pendingRequests("")),
		"review":   s.reviewItems(),
		"next":     s.nextItems(),
	})
}

func (s *server) pushKey(w http.ResponseWriter, r *http.Request) {
	if s.Push == nil {
		webutil.Error(w, http.StatusNotFound, "web push is off")
		return
	}
	webutil.WriteJSON(w, http.StatusOK, map[string]string{"key": s.Push.PublicKey()})
}

// pushSubscribe takes a PushSubscription's JSON, as the browser gives it.
func (s *server) pushSubscribe(w http.ResponseWriter, r *http.Request) {
	if s.Push == nil {
		webutil.Error(w, http.StatusNotFound, "web push is off")
		return
	}
	var in struct {
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.Push.Subscribe(webpush.Subscription{Endpoint: in.Endpoint, P256dh: in.Keys.P256dh, Auth: in.Keys.Auth}); err != nil {
		webutil.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	webutil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *server) pushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	if s.Push == nil {
		webutil.Error(w, http.StatusNotFound, "web push is off")
		return
	}
	var in struct {
		Endpoint string `json:"endpoint"`
	}
	if !decode(w, r, &in) {
		return
	}
	s.Push.Unsubscribe(in.Endpoint)
	webutil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// shownTypes are the only types a kept file is served as, by its
// extension: an image's type was checked from its bytes when it was kept.
// Any other file a user attached is .bin, served only as a download.
var shownTypes = func() map[string]string {
	t := map[string]string{".txt": "text/plain; charset=utf-8", ".bin": "application/octet-stream"}
	for mime, ext := range chat.ImageTypes() {
		t[ext] = mime
	}
	return t
}()

// chatShown serves the kept copy of a file an agent showed or the user
// attached. It's served as inert content in a sandbox of its own: even
// opened directly, it can't run script with the UI's origin.
func (s *server) chatShown(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ctype, ok := shownTypes[path.Ext(name)]
	if !ok {
		http.NotFound(w, r)
		return
	}
	b, err := s.Backend.ChatShown(r.PathValue("id"), name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'; sandbox")
	if path.Ext(name) == ".bin" {
		// Its own name, if the link gave one, so it downloads as itself.
		disp := mime.FormatMediaType("attachment", map[string]string{"filename": r.URL.Query().Get("name")})
		if disp == "" || r.URL.Query().Get("name") == "" {
			disp = "attachment"
		}
		h.Set("Content-Disposition", disp)
	} else {
		h.Set("Content-Disposition", "inline")
	}
	h.Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Write(b)
}
