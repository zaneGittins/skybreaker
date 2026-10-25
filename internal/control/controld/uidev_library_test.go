package controld

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/zaneGittins/skybreaker/internal/assistant"
)

// devLibrary fills pip's library: two podcasts with chapters, a reading
// list as a PDF, a report in Markdown and a page it made, some heard or
// read, so each layout and state has something to show.
func devLibrary(s *assistant.Store) {
	p := func(v float64) *float64 { return &v }
	b := func(v bool) *bool { return &v }
	podcast, issues, n14, n0 := assistant.LayoutPodcast, assistant.LayoutIssues, 14, 0
	brief, about := "Morning briefing", "Every day at 7"
	s.SetCollection("pip", brief, assistant.CollectionPatch{Layout: &podcast, About: &about, Keep: &n14, Speed: p(1.25)}, nil)
	weekly, wabout := "Weekly podcast", "Sundays"
	s.SetCollection("pip", weekly, assistant.CollectionPatch{Layout: &podcast, About: &wabout, KeepDays: &n0}, &assistant.Blob{Data: devCover(), Ext: ".png"})
	reading := "Reading"
	s.SetCollection("pip", reading, assistant.CollectionPatch{Layout: &issues}, nil)

	share := func(sh assistant.Share) assistant.Item {
		it, _, err := s.ShareFile("pip", "", sh)
		if err != nil {
			panic(err)
		}
		return it
	}
	days := []string{"Thursday, October 22", "Friday, October 23", "Saturday, October 24", "Sunday, October 25"}
	for i, day := range days {
		it := share(assistant.Share{Item: assistant.Item{Kind: assistant.KindAudio, Name: "brief.wav", Title: day, Collection: brief,
			Summary:  []string{"Clear and cold; the dentist at 3", "Rain until noon, then bike weather", "Farmers market, bread at 10", "Rain from 4, two meetings after lunch"}[i],
			Chapters: []assistant.Chapter{{At: 0, Title: "Weather"}, {At: 20, Title: "Calendar"}, {At: 45, Title: "Worth knowing"}}},
			Data: devAudio(70+5*i, 330+30*float64(i)), Ext: ".wav"})
		switch i {
		case 0, 1:
			s.SetItemState("pip", it.ID, assistant.ItemState{Finished: b(true), Duration: p(float64(70 + 5*i))})
		case 2:
			s.SetItemState("pip", it.ID, assistant.ItemState{Progress: p(62), Duration: p(80)})
		}
	}
	old := share(assistant.Share{Item: assistant.Item{Kind: assistant.KindAudio, Name: "weekly-1018.wav", Title: "Moving the server rack, and October's books", Collection: weekly},
		Data: devAudio(150, 262), Ext: ".wav"})
	s.SetItemState("pip", old.ID, assistant.ItemState{Finished: b(true), Duration: p(150)})
	ep := share(assistant.Share{Item: assistant.Item{Kind: assistant.KindAudio, Name: "weekly-podcast.wav", Title: "This week: sourdough, home labs, the week ahead", Collection: weekly,
		Summary: "Why the starter stalled, the new NAS, and three things to plan",
		Chapters: []assistant.Chapter{{At: 0, Title: "Intro and the week in one minute"}, {At: 25, Title: "Sourdough: why the starter stalled"},
			{At: 70, Title: "Home lab: the new NAS and backups"}, {At: 120, Title: "Calendar: three things to plan"}, {At: 160, Title: "Books and links"}}},
		Data: devAudio(185, 294), Ext: ".wav",
		Transcript: []byte("Intro and the week in one minute.\n\nGood morning. This week: why the sourdough starter stalled, the new NAS and how backups work now, and three things to plan before the weekend.\n\nSourdough.\n\nThe kitchen dropped to 17 degrees overnight, which slows the yeast. Feed it at a warmer spot, or use water at 30 degrees.\n\nHome lab.\n\nThe NAS is up. Nightly snapshots run at 2, and the offsite copy goes out at 3.\n"),
	})
	s.SetItemState("pip", ep.ID, assistant.ItemState{Progress: p(78), Duration: p(185)})

	pdf := share(assistant.Share{Item: assistant.Item{Kind: assistant.KindPDF, Name: "reading-list.pdf", Title: "Your reading list for the week", Collection: reading,
		Summary: "Eight articles, longest first. Two were paywalled, so I saved the text"},
		Data: devPDF("Reading list, week 43", 6), Ext: ".pdf"})
	s.SetItemState("pip", pdf.ID, assistant.ItemState{Progress: p(3)})
	md := share(assistant.Share{Item: assistant.Item{Kind: assistant.KindText, Name: "insurance.md", Title: "Home insurance, compared", Collection: reading,
		Summary: "Three quotes side by side; the middle one covers the bikes"},
		Data: []byte(devInsurance), Ext: ".md"})
	s.SetItemState("pip", md.ID, assistant.ItemState{Finished: b(true)})
	share(assistant.Share{Item: assistant.Item{Kind: assistant.KindView, Name: "plan.html", Title: "Training plan, week 6 of 12", Collection: "Training plan",
		Summary: "18 of 31 km done; tempo run tomorrow"},
		Data: []byte(devPlan), Ext: ".html"})
}

// devBackdate spreads pip's library and updates over the last days, as a
// week of routines would have left them.
func devBackdate(dataDir string) {
	path := filepath.Join(dataDir, "assistants.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	var st map[string]any
	json.Unmarshal(raw, &st)
	pip := st["assistants"].(map[string]any)["pip"].(map[string]any)
	ago := map[string]time.Duration{
		"Thursday, October 22": 81 * time.Hour, "Friday, October 23": 57 * time.Hour, "Saturday, October 24": 33 * time.Hour,
		"Sunday, October 25": 2 * time.Hour, "Moving the server rack, and October's books": 8 * 24 * time.Hour,
		"This week: sourdough, home labs, the week ahead": 26 * time.Hour, "Your reading list for the week": 5 * time.Hour,
		"Home insurance, compared": 50 * time.Hour, "Training plan, week 6 of 12": 30 * time.Minute,
	}
	at := func(m map[string]any, key string) {
		if d, ok := ago[m[key].(string)]; ok {
			m["at"] = time.Now().Add(-d).UTC().Format(time.RFC3339Nano)
		}
	}
	for _, it := range pip["library"].([]any) {
		at(it.(map[string]any), "title")
	}
	ups := pip["updates"].([]any)
	for _, u := range ups {
		at(u.(map[string]any), "text")
	}
	slices.SortStableFunc(ups, func(a, b any) int {
		return strings.Compare(a.(map[string]any)["at"].(string), b.(map[string]any)["at"].(string))
	})
	out, _ := json.Marshal(st)
	os.WriteFile(path, out, 0o600)
}

// devAudio is secs of a soft tone at freq Hz, pulsing, as 8 kHz mono WAV:
// small, but long enough to seek in and resume.
func devAudio(secs int, freq float64) []byte {
	const rate = 8000
	n := rate * secs
	var b bytes.Buffer
	le := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString("RIFF")
	le(uint32(36 + n))
	b.WriteString("WAVEfmt ")
	le(uint32(16))
	le(uint16(1))
	le(uint16(1))
	le(uint32(rate))
	le(uint32(rate))
	le(uint16(1))
	le(uint16(8))
	b.WriteString("data")
	le(uint32(n))
	for i := range n {
		t := float64(i) / rate
		env := 0.5 + 0.5*math.Sin(2*math.Pi*0.5*t)
		b.WriteByte(byte(128 + 18*env*math.Sin(2*math.Pi*freq*t)))
	}
	return b.Bytes()
}

// devCover is a warm square, the weekly podcast's cover art.
func devCover() []byte {
	const size = 256
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := range size {
		for x := range size {
			t := float64(x+y) / (2 * size)
			img.Set(x, y, color.RGBA{uint8(233 - 93*t), uint8(197 - 123*t), uint8(140 - 96*t), 255})
		}
	}
	for y := 120; y < 200; y++ {
		for x := 64; x < 192; x++ {
			dx, dy := float64(x-128)/64, float64(y-150)/50
			if dy < 0 && dx*dx+dy*dy > 1 {
				continue
			}
			img.Set(x, y, color.RGBA{244, 223, 184, 255})
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

// devPDF is a small real PDF: a title page and pages of lines.
func devPDF(title string, pages int) []byte {
	var objs []string
	add := func(s string) int { objs = append(objs, s); return len(objs) }
	add("<< /Type /Catalog /Pages 2 0 R >>")
	add("") // the page tree, once its pages are known
	font := add("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	var kids []string
	for pg := 1; pg <= pages; pg++ {
		var c strings.Builder
		fmt.Fprintf(&c, "BT /F1 22 Tf 56 760 Td (%s) Tj ET\n", title)
		fmt.Fprintf(&c, "BT /F1 13 Tf 56 730 Td (Page %d of %d) Tj ET\n", pg, pages)
		for l := range 28 {
			fmt.Fprintf(&c, "BT /F1 11 Tf 56 %d Td (%d.%d  Why sourdough needs time, and what the cold does to a starter.) Tj ET\n", 690-l*22, pg, l+1)
		}
		content := add(fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", c.Len(), c.String()))
		page := add(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>", font, content))
		kids = append(kids, fmt.Sprintf("%d 0 R", page))
	}
	objs[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), pages)
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return b.Bytes()
}

const devInsurance = `# Home insurance, compared

Three quotes for the flat, all with the same excess.

## The short version

The **middle quote** is the one: it covers the bikes away from home, and costs only a little more.

| Insurer | Per year | Bikes away from home | Excess |
| --- | --- | --- | --- |
| Harbour | 212 | No | 250 |
| Lindqvist | 238 | Yes, up to 3,000 | 250 |
| Oakley | 301 | Yes, up to 5,000 | 250 |

## Harbour

Cheapest, but bikes are covered only at home. Claims are by phone only.

## Lindqvist

Bikes are covered anywhere up to 3,000, which covers both. Claims online, usually settled in a week.

## Oakley

The most cover, but the extra 2,000 for bikes is more than you need.

## What I'd do

Switch to Lindqvist when Harbour renews on November 14. I can draft the cancellation letter.
`

const devPlan = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<style>
:root{--bg:#1d1c1a;--card:#262522;--text:#f0ede6;--muted:#9c978c;--accent:#6F8DB3}
:root.light{--bg:#fff;--card:#f3f1ec;--text:#1c1b19;--muted:#6b675f}
body{margin:0;padding:16px;background:var(--bg);color:var(--text);font:14px/1.4 system-ui,sans-serif}
.head{display:flex;justify-content:space-between;align-items:baseline;margin-bottom:12px}
.head b{font-size:16px}.head span{color:var(--muted);font-size:12.5px}
.week{display:grid;grid-template-columns:repeat(auto-fill,minmax(84px,1fr));gap:8px}
.day{border-radius:12px;padding:9px 10px;background:var(--card);min-height:64px;font-size:12px;color:var(--muted);cursor:pointer;border:1.5px solid transparent}
.day b{display:block;color:var(--text);font-size:13px;margin-bottom:6px}
.day.done{background:color-mix(in srgb,var(--accent) 22%,var(--card))}
.day.today{border-color:var(--accent)}
.day i{font-style:normal}.day i::before{content:'';display:inline-block;width:6px;height:6px;border-radius:50%;background:var(--accent);margin-right:5px;vertical-align:1px}
.bar{height:6px;border-radius:6px;background:var(--card);margin-top:14px;overflow:hidden}.bar i{display:block;height:100%;background:var(--accent)}
</style></head><body>
<div class="head"><b>Week 6 of 12: half marathon</b><span id="sum"></span></div>
<div class="week" id="week"></div><div class="bar"><i id="bar"></i></div>
<script>
const days=[["Mon","5 km easy",5,1],["Tue","Intervals",6,1],["Wed","Rest",0,1],["Thu","8 km tempo",8,0],["Fri","5 km easy",5,0],["Sat","Rest",0,0],["Sun","13 km long",13,0]];
function draw(){
  const total=days.reduce((a,d)=>a+d[2],0),done=days.reduce((a,d)=>a+(d[3]?d[2]:0),0);
  document.getElementById('sum').textContent=done+' of '+total+' km done';
  document.getElementById('bar').style.width=(100*done/total)+'%';
  const w=document.getElementById('week');w.textContent='';
  const today=days.findIndex(d=>!d[3]);
  days.forEach((d,i)=>{const e=document.createElement('div');e.className='day'+(d[3]?' done':'')+(i===today?' today':'');
    e.innerHTML='<b></b><i></i>';e.querySelector('b').textContent=d[0];e.querySelector('i').textContent=d[1];
    e.onclick=()=>{d[3]=d[3]?0:1;draw()};w.append(e)});
  parent.postMessage({sky:'progress',value:done/total},'*');
  if(done===total)parent.postMessage({sky:'done'},'*');
  parent.postMessage({sky:'height',value:Math.ceil(document.body.getBoundingClientRect().height)+32},'*');
}
addEventListener('message',e=>{const m=e.data||{};if(m.sky!=='init')return;
  document.documentElement.classList.toggle('light',m.theme==='light');
  if(m.accent)document.documentElement.style.setProperty('--accent',m.accent);draw()});
draw();
</script></body></html>`
