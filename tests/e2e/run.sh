#!/usr/bin/env bash
# End-to-end test with rootless podman and the REAL upstream (read-only requests, a few images).
#
#   podman build --target m3 -t m3:dev .  &&  podman build --target e2e-proxy -t m3-e2e-proxy:dev .
#   tests/e2e/run.sh
#
# What it proves
#   * with UPSTREAM_PROXY set, ALL ingest traffic leaves through the proxy: the ingest container
#     sits on an --internal network that has no route to the internet at all;
#   * http:// proxies, https:// proxies (self-signed certificate needs UPSTREAM_PROXY_INSECURE),
#     proxy authentication, and that the proxy password never reaches the logs;
#   * the web role runs with a read-only root file system, a read-only data mount, no capabilities
#     and no network, and answers correctly.
set -u
IMG=${IMG:-localhost/m3:dev}
PROXY_IMG=${PROXY_IMG:-localhost/m3-e2e-proxy:dev}
CLIENT_IMG=${CLIENT_IMG:-docker.io/library/python:3.12-slim}
SUFFIX=$$
NET_INT=m3e2e-int-$SUFFIX
NET_EXT=m3e2e-ext-$SUFFIX
WORK=$(mktemp -d)
UIDGID="$(id -u):$(id -g)"
PASS=0; FAIL=0

# no capabilities, read-only root, unprivileged, files belong to the invoking user
HARD=(--read-only --cap-drop=ALL --security-opt no-new-privileges --userns=keep-id --user "$UIDGID")

ok()   { PASS=$((PASS+1)); printf '  ok    %s\n' "$1"; }
bad()  { FAIL=$((FAIL+1)); printf '  FAIL  %s\n' "$1"; }
check(){ if [ "$1" = 0 ]; then ok "$2"; else bad "$2"; fi; }

cleanup() {
  podman rm -f -t 0 m3proxy-tls m3proxy-http m3proxy-auth m3web >/dev/null 2>&1
  podman network rm -f "$NET_INT" "$NET_EXT" >/dev/null 2>&1
  rm -rf "$WORK"
}
trap cleanup EXIT

podman network create --internal "$NET_INT" >/dev/null || exit 1
podman network create "$NET_EXT" >/dev/null || exit 1

for spec in "m3proxy-tls:-listen :8443 -tls" "m3proxy-http:-listen :3128" "m3proxy-auth:-listen :3128 -auth alice:s3cr3tPASS"; do
  name=${spec%%:*}; args=${spec#*:}
  # shellcheck disable=SC2086
  podman run -d --name "$name" --network "$NET_INT" --network "$NET_EXT" --read-only --cap-drop=ALL "$PROXY_IMG" $args >/dev/null || exit 1
done
sleep 1

ingest() { # <data dir> <extra podman args...> -- <env assignments...>
  local dir=$1; shift
  podman run --rm "${HARD[@]}" -v "$dir:/data:z" -e M3_IMAGES_PER_RUN=4 -e M3_REQUEST_DELAY_MS=150 "$@" "$IMG" ingest daily
}

echo "== 0. the internal network really has no way out"
podman run --rm --network "$NET_INT" "$CLIENT_IMG" python3 -c "
import urllib.request,sys
try:
    urllib.request.urlopen('https://nemzetiarchivum.hu', timeout=6); sys.exit(1)
except Exception: sys.exit(0)" >/dev/null 2>&1
check $? "an --internal container cannot reach the internet by itself"

echo "== 1. ingest on the internal network WITHOUT a proxy must fail (no own route)"
mkdir -p "$WORK/direct-int"
ingest "$WORK/direct-int" --network "$NET_INT" >"$WORK/out.direct-int" 2>&1
[ $? -ne 0 ]; check $? "no proxy + no route -> the ingest fails"
[ ! -e "$WORK/direct-int/m3.sqlite" ]; check $? "nothing was published"

echo "== 2. https proxy with a self-signed certificate"
mkdir -p "$WORK/tls-strict"
ingest "$WORK/tls-strict" --network "$NET_INT" -e UPSTREAM_PROXY=https://m3proxy-tls:8443 >"$WORK/out.tls-strict" 2>&1
[ $? -ne 0 ]; check $? "refused by default (certificate not trusted)"
grep -qi "certificate" "$WORK/out.tls-strict"; check $? "the log names the certificate problem"

mkdir -p "$WORK/tls-insecure"
ingest "$WORK/tls-insecure" --network "$NET_INT" -e UPSTREAM_PROXY=https://m3proxy-tls:8443 -e UPSTREAM_PROXY_INSECURE=1 >"$WORK/out.tls-insecure" 2>&1
check $? "works with UPSTREAM_PROXY_INSECURE=1 (all traffic through the proxy)"
[ -s "$WORK/tls-insecure/m3.sqlite" ]; check $? "a database was published"
ls "$WORK/tls-insecure/images"/*.jpg >/dev/null 2>&1; check $? "cover images were downloaded through the proxy"
grep -q "NOT verified" "$WORK/out.tls-insecure"; check $? "the insecure mode is announced in the log"

echo "== 3. plain http proxy"
mkdir -p "$WORK/http"
ingest "$WORK/http" --network "$NET_INT" -e UPSTREAM_PROXY=http://m3proxy-http:3128 >"$WORK/out.http" 2>&1
check $? "works through an http:// proxy"

echo "== 4. proxy authentication"
mkdir -p "$WORK/auth-bad"
ingest "$WORK/auth-bad" --network "$NET_INT" -e UPSTREAM_PROXY=http://alice:WRONGPASS@m3proxy-auth:3128 >"$WORK/out.auth-bad" 2>&1
[ $? -ne 0 ]; check $? "wrong credentials are refused (407)"
mkdir -p "$WORK/auth-ok"
ingest "$WORK/auth-ok" --network "$NET_INT" -e UPSTREAM_PROXY=http://alice:s3cr3tPASS@m3proxy-auth:3128 >"$WORK/out.auth-ok" 2>&1
check $? "correct credentials work"
! grep -q -e "WRONGPASS" -e "s3cr3tPASS" "$WORK"/out.auth-bad "$WORK"/out.auth-ok; check $? "the proxy password never appears in the log"

echo "== 5. what the proxies saw"
for p in m3proxy-tls m3proxy-http m3proxy-auth; do
  targets=$(podman logs "$p" 2>/dev/null | python3 -c "
import sys,json
t=set()
for l in sys.stdin:
    try: e=json.loads(l)
    except Exception: continue
    if e.get('result')=='200': t.add(e['target'])
print(' '.join(sorted(t)))")
  [ "$targets" = "nemzetiarchivum.hu:443" ]; check $? "$p tunnelled only to nemzetiarchivum.hu:443 (saw: $targets)"
done
podman logs m3proxy-tls 2>/dev/null | grep -q "Chrome/"; check $? "the CONNECT request carries the browser User-Agent"

echo "== 6. baseline: direct ingest on a network with a route"
mkdir -p "$WORK/direct-ext"
ingest "$WORK/direct-ext" --network "$NET_EXT" >"$WORK/out.direct-ext" 2>&1
check $? "direct ingest works where a route exists"

echo "== 7. the web role: read-only root fs, read-only data, no capabilities, no outbound network"
DATA="$WORK/tls-insecure"
# the checks below send ~30 requests from one client in a burst: raise the rate limit for them
# (the limiter itself is covered by the unit tests)
podman run -d --name m3web --network "$NET_INT" "${HARD[@]}" -e M3_RATE_LIMIT=100 -e M3_RATE_BURST=500 -v "$DATA:/data:ro,z" "$IMG" serve >/dev/null
sleep 2
podman exec m3web /m3 healthcheck; check $? "healthcheck inside the hardened container"

cat >"$WORK/check.py" <<'PY'
import http.client, re, sys
fails = 0
def req(method, path, headers=None):
    c = http.client.HTTPConnection("m3web", 8080, timeout=8)
    c.request(method, path, headers=headers or {})
    r = c.getresponse(); body = r.read(); h = {k.lower(): v for k, v in r.getheaders()}
    c.close(); return r.status, h, body
def t(name, cond):
    global fails
    print(("  ok    " if cond else "  FAIL  ") + name)
    if not cond: fails += 1
s, h, b = req("GET", "/")
t("GET / -> 200 html", s == 200 and h["content-type"].startswith("text/html"))
t("the page lists programs", "találat" in b.decode())
t("no script tag, no third party host", b"<script" not in b and b"googleapis" not in b and b"unpkg" not in b)
t("every program links to its page on the M3 site", b"https://nemzetiarchivum.hu/m3/M3-" in b and b'rel="noopener noreferrer"' in b)
t("an availability badge is shown (the daily run records the playable flag)", b"avail--" in b)
sf, _, bf = req("GET", "/?elerheto=1"); t("the 'only available' filter answers", sf == 200 and b"avail--no" not in bf)
for hd in ["content-security-policy","x-content-type-options","referrer-policy","strict-transport-security","x-frame-options","cross-origin-opener-policy"]:
    t("header " + hd, hd in h)
t("no cookies", "set-cookie" not in h)
m = re.search(rb'/img/(M3-[A-Z0-9]+)\.jpg', b)
t("a cover image is referenced", m is not None)
if m:
    s, h, img = req("GET", "/img/%s.jpg" % m.group(1).decode())
    t("cover image served from the local file", s == 200 and h["content-type"] == "image/jpeg" and img[:3] == b"\xff\xd8\xff")
for path, want in [("/nope", (404,)), ("/0", (404,)), ("/img/..%2F..%2Fetc%2Fpasswd", (404,)), ("/static/", (404,)), ("/img/M3-NOPE.jpg", (404,)),
                   ("/static/../x", (301, 307, 308, 404))]:  # the Go mux redirects dot segments to the cleaned path
    s, h, body = req("GET", path)
    t("GET %s -> %s and no content served" % (path, "/".join(map(str, want))), s in want and b"root:" not in body and (s not in (301, 307, 308) or h.get("location", "").startswith("/")))
for meth in ["POST", "PUT", "DELETE", "PATCH"]:
    s, _, _ = req(meth, "/"); t("%s / -> 405" % meth, s == 405)
s, _, b = req("GET", "/?kereses=%22%3E%3Cscript%3Ealert(1)%3C%2Fscript%3E")
t("a hostile search term is escaped", b"<script>alert" not in b)
s, _, b = req("GET", "/?kereses=ab"); t("short term gets a notice", "Legalább 3" in b.decode())
s, _, _ = req("GET", "/static/app.css"); t("static css (status %d)" % s, s == 200)
sys.exit(1 if fails else 0)
PY
podman run --rm --network "$NET_INT" -v "$WORK/check.py:/check.py:ro,z" "$CLIENT_IMG" python3 /check.py | tee "$WORK/web.out"
PY_STATUS=${PIPESTATUS[0]}
PASS=$((PASS + $(grep -c '^  ok' "$WORK/web.out")))
FAIL=$((FAIL + $(grep -c '^  FAIL' "$WORK/web.out")))
check $PY_STATUS "all web checks together"
podman rm -f -t 0 m3web >/dev/null 2>&1

echo "-- web role with NO network at all"
podman run -d --name m3web --network none "${HARD[@]}" -v "$DATA:/data:ro,z" "$IMG" serve >/dev/null
sleep 2
podman exec m3web /m3 healthcheck; check $? "the web role works with --network none"
podman rm -f -t 0 m3web >/dev/null 2>&1

echo "== 8. files on the host"
owner=$(stat -c %U "$DATA/m3.sqlite"); [ "$owner" = "$(id -un)" ]; check $? "m3.sqlite belongs to the host user ($owner)"
ls "$DATA" | grep -qE -e '-wal$|-shm$|-journal$|\.new$'; [ $? -ne 0 ]; check $? "no wal/shm/journal/work files next to the database"
if command -v sqlite3 >/dev/null; then sqlite3 "$DATA/m3.sqlite" 'select count(*) from programs' | sed 's/^/  programs in the database: /'; fi

echo
echo "e2e: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
