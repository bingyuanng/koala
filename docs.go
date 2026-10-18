package koala

import "net/http"

// The bundled, dependency-free viewer keeps documentation usable offline.
// It renders the generated OpenAPI document using textContent, never HTML from
// API descriptions. There is no remote script, stylesheet, or source-file read.
const docsHTML = `<!doctype html><html><head><meta charset="utf-8"><title>API documentation</title><style>body{font:16px system-ui;max-width:1000px;margin:40px auto;padding:20px}pre{white-space:pre-wrap;background:#f4f4f4;padding:16px}summary{cursor:pointer;padding:12px}</style></head><body><h1>API documentation</h1><a href="openapi.json">OpenAPI JSON</a><main id="api">Loading specification…</main><script>fetch('openapi.json').then(r=>{if(!r.ok)throw Error('Specification unavailable');return r.json()}).then(s=>{const a=document.getElementById('api');a.textContent='';for(const [path,methods] of Object.entries(s.paths||{})){for(const [verb,operation] of Object.entries(methods)){if(!['get','post','put','patch','delete','options','head'].includes(verb))continue;const d=document.createElement('details'),t=document.createElement('summary'),p=document.createElement('pre');t.textContent=verb.toUpperCase()+' '+path+' '+(operation.summary||'');p.textContent=JSON.stringify(operation,null,2);d.append(t,p);a.append(d)}}const h=document.createElement('h2');h.textContent='Schemas';const p=document.createElement('pre');p.textContent=JSON.stringify(s.definitions||{},null,2);a.append(h,p)}).catch(()=>{document.getElementById('api').textContent='Unable to load the API specification.'})</script></body></html>`

func serveDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(docsHTML))
}
