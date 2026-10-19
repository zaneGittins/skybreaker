package api

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

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

func (s *server) chatSend(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text string `json:"text"`
	}
	if !decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Text) == "" {
		webutil.Error(w, http.StatusBadRequest, "empty message")
		return
	}
	if err := s.Backend.ChatSend(r.Context(), r.PathValue("id"), in.Text); err != nil {
		chatFail(w, err)
		return
	}
	webutil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// chatFail maps a chat error: unknown sessions are 404, anything else the
// user can act on is 400.
func chatFail(w http.ResponseWriter, err error) {
	if errors.Is(err, chat.ErrTerminalLogin) {
		webutil.WriteJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "terminal": true})
		return
	}
	if strings.Contains(err.Error(), "not found") {
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
	if err := s.Backend.ChatModel(r.Context(), r.PathValue("id"), in.Model); err != nil {
		chatFail(w, err)
		return
	}
	webutil.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
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

// shownTypes are the only types a shown file is served as, by the kept
// copy's extension: its type was checked from its bytes when it was shown.
var shownTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp",
	".txt": "text/plain; charset=utf-8",
}

// chatShown serves the kept copy of a file an agent showed. It comes from
// a sandbox, so it's served as inert content in a sandbox of its own: even
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
	h.Set("Content-Disposition", "inline")
	h.Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Write(b)
}
