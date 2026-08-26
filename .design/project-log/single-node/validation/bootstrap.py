import threading,http.server,urllib.request,json,sys,os
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"ok")
    def log_message(self,*a):
        pass
s=http.server.HTTPServer(("",8080),H)
threading.Thread(target=s.serve_forever,daemon=True).start()
print("Health check on :8080",flush=True)
try:
    r=urllib.request.Request("http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token",headers={"Metadata-Flavor":"Google"})
    t=json.loads(urllib.request.urlopen(r,timeout=10).read())["access_token"]
    r2=urllib.request.Request("https://storage.googleapis.com/ptone-experiments-instance-gym/validation/delete_timeout_validation_v2.py",headers={"Authorization":"Bearer "+t})
    code=urllib.request.urlopen(r2,timeout=30).read()
    print(f"Downloaded {len(code)} bytes",flush=True)
    exec(compile(code,"validate.py","exec"))
except Exception as e:
    print(f"Bootstrap error: {e}",flush=True)
    import traceback
    traceback.print_exc()
    print("Keeping alive for diagnostics...",flush=True)
    import time
    while True:
        time.sleep(3600)
