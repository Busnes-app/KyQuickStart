package edge

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"

	"github.com/Busnes-app/kyquickstart/internal/stack"
)

// The nginx edge's pods read their configuration from one ConfigMap mounted at NginxConfDir:
// NginxMainFile, written by the app's deploy, and one <key>.server file per enabled route,
// written by its route step. A removed route's file is renamed <key>.disabled, which nginx
// does not include. TLS Secrets are mounted at NginxTLSDir/<secret name>.
const (
	NginxConfDir  = "/etc/nginx/kyq"
	NginxTLSDir   = "/etc/nginx/tls"
	NginxMainFile = "nginx.conf"
	// NginxReadyPath answers 204 on port 80 once nginx serves: the pods' readiness probe.
	NginxReadyPath = "/kyq-ready"
	nginxRunDir    = "/tmp/kyq" // the pod's own: resolver, generation and temporary files
	nginxLocal     = "127.0.0.1:8081"
	enabledExt     = ".server"
	disabledExt    = ".disabled"
)

// NginxMain is the whole configuration but the routes. Timeouts, body size and TLS settings
// are NPM 2.16's, so a route behaves the same behind either proxy. Port 80 serves nothing: it
// redirects every request to the host it asked for (as NPM's forced HTTPS does) and answers the
// readiness probe, and an
// unknown TLS name gets no handshake. The loopback listener answers the generation the workers
// run, which the installer reads through exec.
const NginxMain = `worker_processes 2;
error_log stderr warn;
pid ` + nginxRunDir + `/nginx.pid;
events {
  worker_connections 4096;
}
http {
  include /etc/nginx/mime.types;
  default_type application/octet-stream;
  client_body_temp_path ` + nginxRunDir + `/body;
  proxy_temp_path ` + nginxRunDir + `/proxy;
  fastcgi_temp_path ` + nginxRunDir + `/fastcgi;
  uwsgi_temp_path ` + nginxRunDir + `/uwsgi;
  scgi_temp_path ` + nginxRunDir + `/scgi;
  access_log /dev/stdout;
  server_tokens off;
  sendfile on;
  tcp_nopush on;
  tcp_nodelay on;
  gzip on;
  keepalive_timeout 90s;
  proxy_connect_timeout 90s;
  proxy_send_timeout 90s;
  proxy_read_timeout 90s;
  client_max_body_size 2000m;
  server_names_hash_bucket_size 512;
  ssl_protocols TLSv1.2 TLSv1.3;
  ssl_ciphers ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384:ECDHE-ECDSA-CHACHA20-POLY1305:ECDHE-RSA-CHACHA20-POLY1305:DHE-RSA-AES128-GCM-SHA256:DHE-RSA-AES256-GCM-SHA384;
  ssl_prefer_server_ciphers off;
  ssl_session_cache shared:SSL:10m;
  ssl_session_timeout 5m;
  include ` + nginxRunDir + `/resolver.conf;
  server {
    listen 80 default_server;
    location = ` + NginxReadyPath + ` {
      access_log off;
      return 204;
    }
    location / {
      return 301 https://$host$request_uri;
    }
  }
  server {
    listen 443 ssl default_server;
    ssl_reject_handshake on;
  }
  server {
    listen ` + nginxLocal + `;
    access_log off;
    location = /generation {
      include ` + nginxRunDir + `/generation.conf;
    }
  }
  include ` + NginxConfDir + `/*` + enabledExt + `;
}
`

// NginxCommand is the pod's process: it starts nginx and reloads it whenever the mounted
// configuration or a certificate changes, so neither a route change nor a renewal restarts a
// pod or drops a connection. A configuration nginx rejects is never loaded: the running one
// keeps serving and the reason is kept for the installer. The generation is the hash
// NginxGeneration computes from the same files.
var NginxCommand = []string{"/bin/sh", "-c", `set -eu
conf=` + NginxConfDir + ` run=` + nginxRunDir + `
mkdir -p "$run"
: > "$run/error"
awk 'BEGIN { printf "resolver" } /^nameserver/ { a = $2; if (a ~ /:/) a = "[" a "]"; printf " %s", a } END { print " valid=10s ipv6=off;" }' /etc/resolv.conf > "$run/resolver.conf"
pid= loaded=
stop() {
  # Stay up while the Service stops sending here, then let open requests finish.
  [ -z "$pid" ] || { sleep 5; kill -QUIT "$pid"; wait "$pid" || true; }
  exit 0
}
trap stop TERM QUIT INT
while :; do
  gen=$(cat "$conf/` + NginxMainFile + `" "$conf"/*` + enabledExt + ` 2>/dev/null | sha256sum | cut -c1-64)
  now="$gen $(cat ` + NginxTLSDir + `/*/tls.crt 2>/dev/null | sha256sum | cut -c1-64)"
  if [ "$now" != "$loaded" ]; then
    printf 'return 200 "%s";\n' "$gen" > "$run/generation.conf"
    if nginx -t -q -c "$conf/` + NginxMainFile + `" 2> "$run/error.new"; then
      if [ -z "$pid" ]; then
        nginx -c "$conf/` + NginxMainFile + `" -g 'daemon off;' &
        pid=$!
      else
        kill -HUP "$pid"
      fi
      loaded=$now
      : > "$run/error"
    elif ! cmp -s "$run/error.new" "$run/error"; then
      cat "$run/error.new" >&2
      cp "$run/error.new" "$run/error"
    fi
  fi
  [ -z "$pid" ] || kill -0 "$pid" 2>/dev/null || exit 1
  sleep 2 &
  wait $! || true
done
`}

// NginxLoaded prints the generation the pod's nginx workers run, then the reason it rejected
// a newer configuration, if it did.
var NginxLoaded = []string{"/bin/sh", "-c", `curl -s -m 3 http://` + nginxLocal + `/generation; echo; cat ` + nginxRunDir + `/error 2>/dev/null`}

// NginxGeneration names the configuration nginx loads from files: the main file and every
// enabled route, in the order the pod's shell reads them.
func NginxGeneration(files map[string]string) string {
	h := sha256.New()
	h.Write([]byte(files[NginxMainFile]))
	var keys []string
	for k := range files {
		if strings.HasSuffix(k, enabledExt) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	for _, k := range keys {
		h.Write([]byte(files[k]))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ManagedTLSSecret is the Secret cert-manager writes for the edge certificate named cert.
func ManagedTLSSecret(cert string) string { return "kyq-tls-" + cert }

// tlsSecret is the Secret holding r's certificate.
func tlsSecret(r stack.Route) string {
	if r.Certificate != "" {
		return ManagedTLSSecret(r.Certificate)
	}
	return r.CertificateSecret
}

// TLSSecrets are the Secrets e's routes serve certificates from, sorted: the edge's pods mount
// exactly these.
func TLSSecrets(e stack.Edge) []string {
	var out []string
	for _, r := range e.Routes {
		out = append(out, tlsSecret(r))
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// MountedTLSSecrets are the Secrets the edge's pods must mount for e: its routes', and those
// the enabled files still name. A pod replaced before a route's file is rewritten or disabled
// would otherwise start with a file whose certificate it cannot read, and not start at all.
// A Secret only an old file named is dropped by the apply after the one that rewrote it.
func MountedTLSSecrets(e stack.Edge, files map[string]string) []string {
	out := TLSSecrets(e)
	for name, file := range files {
		if !strings.HasSuffix(name, enabledExt) {
			continue
		}
		for _, line := range strings.Split(file, "\n") {
			if rest, ok := strings.CutPrefix(line, "ssl_certificate "+NginxTLSDir+"/"); ok {
				if secret, _, ok := strings.Cut(rest, "/"); ok {
					out = append(out, secret)
				}
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// routeKey names r's file without its extension. One hostname has one key, so two routes
// cannot both serve a name.
func routeKey(hostname string) string {
	sum := sha256.Sum256([]byte(hostname))
	return hex.EncodeToString(sum[:8])
}

// nginxServer is r's file: its ownership marker, then its server block.
func nginxServer(marker string, e stack.Edge, r stack.Route) string {
	dir := NginxTLSDir + "/" + tlsSecret(r)
	upstream := r.ForwardScheme + "://" + net.JoinHostPort(r.ForwardHost, strconv.Itoa(r.ForwardPort))
	return marker + fmt.Sprintf("server {\nlisten 443 ssl;\nhttp2 on;\nserver_name %s;\nssl_certificate %s/tls.crt;\nssl_certificate_key %s/tls.key;\n", r.Hostname, dir, dir) +
		serverConfig(e, r, upstream) + "}\n"
}
