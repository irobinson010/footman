// websink serves a page with a focused textarea and logs every change the
// browser reports as JSON lines {"t": ms, "text": full text}. Used for
// Firefox, Chromium/Brave and Electron.
//
//	websink -addr 127.0.0.1:8799 -log /path/web.jsonl
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

const page = `<!doctype html><meta charset=utf-8><title>websink</title>
<textarea id=t autofocus style="width:95vw;height:90vh;font-size:16px"></textarea>
<script>
const t=document.getElementById('t');t.focus();
let q=[],busy=false,seq=0;
function flush(){if(busy||!q.length)return;busy=true;const b=q.splice(0);
 fetch('/log',{method:'POST',body:b.map(x=>JSON.stringify(x)).join('\n')}).finally(()=>{busy=false;flush()});}
t.addEventListener('input',()=>{q.push({seq:++seq,t:Date.now(),text:t.value});flush();});
</script>`

func main() {
	addr := flag.String("addr", "127.0.0.1:8799", "listen address")
	logPath := flag.String("log", "web.jsonl", "log file")
	flag.Parse()
	f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		log.Fatal(err)
	}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, page) })
	http.HandleFunc("/log", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rt := float64(time.Now().UnixMicro()) / 1000
		for _, line := range strings.Split(string(b), "\n") {
			var m map[string]any
			if json.Unmarshal([]byte(line), &m) != nil {
				continue
			}
			m["rt"] = rt // server receive time; browser clocks may be coarsened
			out, _ := json.Marshal(m)
			f.Write(append(out, '\n'))
		}
	})
	http.HandleFunc("/clear", func(w http.ResponseWriter, r *http.Request) { f.Truncate(0) })
	log.Printf("websink on http://%s, logging to %s", *addr, *logPath)
	log.Fatal(http.ListenAndServe(*addr, nil))
}
