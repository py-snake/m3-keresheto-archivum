# CLAUDE.md – AI munkafüzet (élő dokumentum)

> **Ezt a fájlt a Claude Code minden session elején automatikusan betölti. Ez az egyetlen „igazság forrása" a projekt állapotáról.**
> Minden session: (1) elolvassa, (2) munka közben frissen tartja, (3) a végén a [Változásnaplóba](#10-változásnapló) ír egy sort.

- **Branch:** `go`. A `master` a lezárt PHP-verzió (referencia, **nem érdekes**: a felhasználó szerint „a php nem érdekel"). Ezen az ágon a PHP kód már nincs; tanulságai: [docs/PHP-HISTORY.md](docs/PHP-HISTORY.md).
- **Állapot (2026-11-05):** a Go program **kész és tesztelt**: `go vet`, `go test -race ./...`, `govulncheck` tiszta, `tests/e2e/run.sh` **48/48**, valós M3 adaton futtatva. Nem commitolt/pusholt kód nincs a munkában, kivéve ahol a naplóban jelezve.
- **Tervdokumentum:** [docs/GO-DESIGN.md](docs/GO-DESIGN.md). **Felhasználói leírás (magyar):** [README.md](README.md).
- **Utolsó mérés a valós rendszeren:** 2026-11-05.

## 1. Munkaszabályok (a felhasználó utasításai)

| Szabály | Forrás |
|---|---|
| **Programkód, kommentek, commit üzenetek, log üzenetek angolul**; **UI és README magyarul** | felhasználó, 2026-11-05 |
| Ez a fájl és a tervdokumentum magyarul (a felhasználó olvassa); azonosítók és kódrészletek angolok | döntés |
| **Elsődleges szempont a biztonság.** „Feltörhetetlen" nem garantálható: a [GO-DESIGN 2. fejezete](docs/GO-DESIGN.md#2-mit-jelent-itt-a-feltörhetetlen) mondja ki, mit ígérünk | felhasználó, 2026-11-05 |
| Futás: **podman, egyetlen statikus Go bináris, SQLite fájl a hoszt lemezén** (a hoszt felhasználó menti) | felhasználó, 2026-11-05 |
| **Nincs `/add`** (kérésre felvétel). A web semmit nem ír. Adminisztrációra: `m3 ingest item M3-…` | felhasználó (D1) |
| **Nincs régi adat**: üres adatbázisról indulunk, a mai ablakból; nincs `import` | felhasználó (D6) |
| **A borítóképeket a Go bináris szolgálja ki helyben tárolt fájlból** (a statikus tartalom az exe-be fordítva; a dinamikus képek a `data/images/`-ben, mert az exe build-időben készül) | felhasználó (D3) |
| **Kifelé mindig valódi böngésző User-Agent**, sosem saját azonosító | felhasználó, 2026-11-05 |
| **Kimenő HTTP- és HTTPS-proxy támogatás** (hogy ne a saját IP-ről menjen ki a forgalom), „insecure https" proxyval is | felhasználó, 2026-11-05 |
| Go-ban a **stdlib az elsődleges**; új modul csak indoklással (most **4** beépülő modul) | terv |
| Commit: a felhasználó kérésére (a lezáró „véglegesítsd/készítsük el" kérés commitot is jelent); **push soha nem történt, csak kifejezett kérésre** | felhasználó |
| Tesztadatot/ideiglenes fájlt a scratchpadre, ne a repóba; **harmadik fél szerverét nem támadjuk** (pl. útvonal-bejárás-próbák csak a saját szerverünkön) | tanulság |

## 2. Térkép

```
cmd/m3/main.go            parancsok: serve | ingest daily|weekly|images|item | healthcheck | version
internal/config/          az egész konfiguráció környezeti változókból (UPSTREAM_PROXY, …); alapértelmezett böngésző UA
internal/progid/          az M3-azonosító validálása (EGYETLEN hely; mindenhol ezt használjuk)
internal/model/           közös Program típus
internal/upstream/        kimenő kliens: egy engedélyezett hoszt, proxy-alagút (proxy.go), parser, végpontok
internal/images/          kép: ellenőrzés, dekódolás+újrakódolás, atomi írás, os.Root-os megnyitás
internal/store/           SQLite: séma, reader (csak olvasó + authorizer), writer, Publisher (atomi publikálás), Source (csere-figyelés)
internal/ingest/          daily / weekly / items / images feladatok, megszakító
internal/web/             HTTP: sablonok, middleware (fejlécek, rate limit), kép-kiszolgálás
internal/fakeupstream/    hamis M3 szerver a tesztekhez (tests/fixtures valós felvételeiből)
web/                      beágyazott sablonok és statikus fájlok (embed)
tests/fixtures/           rögzített valós upstream válaszok
tests/e2e/                run.sh (podman, valós upstream) + proxy/ (teszt-CONNECT-proxy)
deploy/quadlet/           systemd+podman egységek (web, cloudflared, ingest időzítők, hálózatok)
Containerfile             digestre rögzített build, scratch végső image (cél: m3 | e2e-proxy)
docs/                     GO-DESIGN.md, PHP-HISTORY.md, spikes/
```

**Adatfolyam.** `m3 ingest …` (külön konténer/timer, kimenő hálózat, **írja** az adatkönyvtárat): lekéri az M3-ból → validál → `store.Publisher.Run`: `m3.sqlite` másolata → módosítás → `integrity_check` + FTS-ellenőrzés + sorszám-ésszerűség → `fsync` → régi fájl keményhivatkozása a `backups/`-ba → **atomi `rename`**. `m3 serve` (web konténer, **nincs kimenő hálózat**, adat `:ro`): `store.Source` figyeli a fájlcserét (30 s), új `Reader`-t nyit, a régit türelmi idő után zárja.

**Az invariáns:** *a publikált `m3.sqlite`-t soha, senki nem módosítja helyben.* Ezért olvasható `immutable=1`-gyel (nincs WAL/lock/melléktermék), a hoszt felhasználó bármikor másolhatja, és egy összeomlás nem rontja el. **A képek** (`images/`) csak bővülnek, és a DB publikálása *előtt* készülnek el.

## 3. Döntések és mért tények (2026-11-05)

- **Driver:** `github.com/ncruces/go-sqlite3` **v0.36.0** (SQLite 3.54.1, WASM→Go gépi fordítás, **CGO nélkül**). FTS5-öt **kapcsolatonként** kell regisztrálni (`fts5.Register`); az `embed` csomag elavult. Beépülő modulok: `go-sqlite3`, `go-sqlite3-wasm/v6`, `julianday`, `golang.org/x/sys`. Bináris ≈17,7 MB, image ≈17,9 MB.
- **Keresés:** FTS5 `trigram case_sensitive 0 remove_diacritics 1`: kis-nagybetű- és ékezetfüggetlen **részszó**-keresés; **<3 karakter nem talál** (a UI üzenetet ad); a felhasználói szöveg mindig egyetlen idézőjelezett kifejezés (sosem FTS-szintaxis). `M3-…` kezdetű szöveg azonosító-keresés (≤50). Találatplafon: 10 000 (`+` jel), oldalplafon 1000.
- **Teljesítmény valós adaton** (962 műsor, `internal/store/latency_test.go`): **p50 1,4 ms, p95 2,7 ms, max 4,7 ms**. DB-méret 10,5 MB (≈11 KB/műsor a trigram-indexszel; 50 000 műsor ≈ 0,5 GB). Kép ≈ 25 KB/db átméretezve 480 px-re.
- **Valós weekly futás:** 8 gyűjtemény, 88 sorozat, 965 azonosító → 962 hiányzó → **962 új, 0 hiba**, 2 perc 40 mp (250/150 ms kérés-szünettel).
- **A csak olvasható mód nem elég:** `ATTACH DATABASE` olvasható kapcsolaton is lefut → **deny-by-default authorizer** (`internal/store/authorizer.go`). Engedett: `SELECT`; `READ` az app- és `programs_fts*` táblákra; függvények `match, count, min, max, coalesce, length, lower, upper`; `PRAGMA query_only` (olvasás/1) és `data_version`/`user_version` (olvasás). Ha egy új lekérdezés „authorization denied"-ot kap, az authorizert kell *szűken* bővíteni, nem kikapcsolni.
- **Upstream:** minden végpont a [PHP-HISTORY.md 6. fejezete](docs/PHP-HISTORY.md) szerint; kiegészítés: a borítókép `https://nemzetiarchivum.hu/images/m3/<id>` (JPEG, 8–41 KB, 280–720 px); **ismeretlen azonosítóra is `HTTP 200`**, 393 bájtos HTML (→ a tartalmat kell ellenőrizni, JPEG-mágia). `item?id=` ismeretlenre `200 + null` (`ErrNotFound`). Sorozatoldal: `page=0 ≡ page=1`, az utolsó utáni üres. Őszinte és böngésző UA-val is kiszolgál.
- **Proxy** (`internal/upstream/proxy.go`): saját `CONNECT`-alagút (http:// és https:// proxy), mert a `net/http` közös TLS-konfigot használna a proxyhoz és a célhoz is; így `UPSTREAM_PROXY_INSECURE` **csak a proxy** tanúsítványát engedi át, a céloldalét **soha**. A cél nevét a proxy oldja fel. Környezeti `HTTP(S)_PROXY`-t **szándékosan nem használunk**. Proxy-hiba esetén **nincs visszaesés közvetlen kapcsolatra**. A jelszó a naplóban/hibában soha (`label()` = séma://hoszt). Bizonyítva a `tests/e2e/run.sh`-ban (`--internal` hálózat).
- **Go mux:** a `..` szegmenses útra **307** átirányítást ad a megtisztított címre (nem 404); tartalmat nem szolgál ki.

## 4. Tanulságok ebből a munkából (ne ismételd)

1. **`INSERT OR IGNORE` elnyeli a `CHECK` hibákat is** (ugyanaz a hibaosztály, mint a PHP `INSERT IGNORE`-ja). Használd: `INSERT … ON CONFLICT (program_id) DO NOTHING`. Teszt: `TestSchemaConstraintsRejectGarbage`.
2. **Authorizer és a driver belső pragmái:** a driver `query_only`-t, az FTS5 `data_version`-t olvassa. Deny-by-default mellett ezeket külön, szűken engedni kell.
3. **A teszt-kiszolgáló cert csak a saját `RootCAs`-szal bízható**: a `fakeupstream` ezt adja (`Server.CAs`).
4. **Sosem pipe-old a tesztet** (`go test | tail`) úgy, hogy a kilépési kódra támaszkodsz: a pipe elnyeli. (A PHP-s munkában emiatt ment le egy törött teszt commitba.)
5. A `json.Marshal` az érvénytelen UTF-8-at U+FFFD-re cseréli; a tisztító az U+FFFD-t is eltávolítja.
6. A `os.OpenRoot` (Go ≥1.24) a szimbolikus linkes kitörést is kizárja; a kép-kiszolgálás erre épül.
7. Harmadik fél szerverén **ne** próbálj támadó jellegű bemenetet (az automatikus engedélyező ezt jogosan tiltotta).

## 5. Ellenőrző receptek

```bash
gofmt -l . ; go vet ./... && go test -race -count=1 ./...        # kimenetet NE pipe-old a kilépési kód miatt
$(go env GOPATH)/bin/govulncheck ./...                            # go install golang.org/x/vuln/cmd/govulncheck@latest
go test -run '^$' -fuzz='^FuzzParseProgram$' -fuzztime=30s ./internal/upstream   # fuzzerek: FuzzParseProgram, FuzzSanitize (images), FuzzSearchTerm (store)
# CSP böngészőben: chromium-browser --headless=new --no-sandbox --enable-logging=stderr --v=0 --virtual-time-budget=4000 --screenshot=out.png http://127.0.0.1:PORT/
#   (a konzol 'violates the following Content Security Policy' sorait keresd; előbb pozitív kontrollal ellenőrizd a módszert)
M3_BENCH_DB=/út/m3.sqlite go test -run TestSearchLatencyOnRealData -v ./internal/store

podman build --target m3 -t localhost/m3:dev . && podman build --target e2e-proxy -t localhost/m3-e2e-proxy:dev .
tests/e2e/run.sh                                                   # valós upstream, izolált podman hálózatok; 48/48 volt
QUADLET_UNIT_DIRS=$PWD/deploy/quadlet /usr/lib/systemd/user-generators/podman-user-generator --dryrun   # Quadlet validálás

# valós adat gyűjtése helyben (a scratchpadre)
M3_DATA_DIR=$S/real M3_IMAGES_PER_RUN=20 go run ./cmd/m3 ingest weekly
```

## 6. Biztonsági kritériumok állapota (GO-DESIGN 13. fejezet)

| # | Állapot | Bizonyíték |
|---|---|---|
| S1 govulncheck | ✅ | „No vulnerabilities found" (2026-11-05) |
| S2 statikus, cgo nélküli | ✅ | `CGO_ENABLED=0`, `go version -m`: 4 modul |
| S3 web fut `--network none`, ro, cap nélkül | ✅ | e2e §7 |
| S4 authorizer tiltásai | ✅ | `TestReaderCannotDoAnythingExceptSelect` (16 tiltott utasítás) |
| S5 FTS-lekérdezésépítő fuzz | ✅ | `TestHostileSearchTermsAreJustText` + `FuzzSearchTerm`: 248 ezer futás 25 mp alatt, 0 találat-eltérés/hiba (2026-11-05) |
| S6 fejlécek minden válaszon | ✅ | `TestSecurityHeadersOnEveryKindOfResponse` (18 eset + 429) |
| S7 csak GET/HEAD, nincs süti | ✅ | ugyanott + e2e |
| S8 slowloris, túlméretes URL/fejléc | ✅ | `TestSlowHeaderClientsAreDisconnected`, `TestOversizedHeadersAreRefused`, 414 |
| S9 rate limit, hamisított fejléc | ✅ | `TestRateLimitIgnoresForgedHeaders…`, `…SeparatesClientsBehindATrustedProxy`, `TestLimiterMemoryIsBounded` |
| S10 XSS | ✅ | `TestEverythingUserControlledIsEscaped` (minden mező, `?nyers` is) |
| S11 ingest hostile fixture-ök | ✅ | `internal/upstream` + `internal/ingest` tesztek, `FuzzParseProgram` |
| S12 összeomlás-teszt | ✅ | `TestCrashAtAnyPoint`: valódi folyamat 6 ponton megölve |
| S13 megszakító | ✅ | `TestCircuitBreakerOnBrokenUpstreamData`, `…AllInvalid…` |
| S14 CSP valódi böngészőben | ✅ | headless Chromium 155 a **valós** adaton (`/`, `/?kereses=`, `/?nyers`): **0 CSP-sértés**; pozitív kontroll (szándékosan sértő oldal) 3 sértést jelzett, tehát a módszer észleli; a képernyőkép szerint a felület helyesen renderel, a képek helyi fájlból |
| S15 image-vizsgálat (trivy/grype) | ❌ nincs megcsinálva | scratch: nincs OS-csomag |
| S16 paritás a PHP-val | ➖ elvetve | a PHP nem érdekes |
| S17 ZAP baseline | ❌ nincs megcsinálva | |
| S18 kép-fixture-ök | ✅ | `internal/images` tesztek, `TestHostileImagesAreNotStored` |
| S19 `/img/` bejárás a saját szerveren | ✅ | `TestImagesAreServedFromTheDataDirectoryOnly` (18 minta, symlinkek) |
| S20 web: nulla kimenő kérés | ✅ | e2e: `--network none` és `--internal` |

## 7. Nyitott teendők (prioritás szerint)

1. [ ] **S15/S17**: image-szkennelés (trivy/grype), OWASP ZAP baseline a futó konténeren (nincs telepítve a gépen).
3. [ ] **Quadlet élesben**: a generátor hibátlan, de az egységeket **nem indítottuk el** (cloudflared token kell). Digestre rögzíteni a cloudflared image-et.
4. [ ] **CSV/gz export** (GO-DESIGN D4): nincs megvalósítva; ha kell, a CSV-injection escape kötelező.
5. [ ] **`image_state = 2`** (az upstreamnek nincs képe) újrapróbálása (pl. 30 nap után): most végleges.
6. [ ] NFC-normalizálás (terv): nincs, mert `golang.org/x/text` új modul lenne; a vezérlő/bidi karakterek szűrése megvan.
7. [ ] `modernc.org/sqlite` mint tartalék driver: **nem mért**; a tároló réteg (`internal/store`) szűk, cserélhető.
8. [ ] Döntések: D12 (átméretezés **megvan**, 480 px, `M3_IMAGE_MAX_WIDTH`), **D13** a harmadik fél borítóképeinek helyi másolata (szerzői jog/ToS) a felhasználó felelőssége, **D14** UA-karbantartás (verzió időnként frissítendő).
9. [ ] Függetlenül végzett biztonsági felülvizsgálat az élesítés előtt (javasolt).
10. [ ] A `master` ág sorsa (PHP) és az `origin` push: a felhasználó dönt.

## 8. Konfiguráció (összefoglaló)

Minden környezeti változóból ([internal/config/config.go](internal/config/config.go), magyar táblázat: [README.md](README.md)). Biztonsági szempontból lényeges: `UPSTREAM_PROXY`, `UPSTREAM_PROXY_INSECURE` (csak https proxyval érvényes), `UPSTREAM_CA_FILE`, `UPSTREAM_USER_AGENT`, `M3_TRUSTED_PROXIES`.

## 9. Ismert „nem hiba" dolgok

- A `web` csomag `time/tzdata`-t tölt (a scratch image-ben nincs zoneinfo): szándékos.
- A CSS (`m3.min.css`) `data:` SVG-ket tartalmaz → a CSP `img-src 'self' data:`.
- A `robots.txt` mindent tilt (az eredeti is): a felhasználó dönthet.
- `GET /static/../x` → 307, nem 404 (Go mux).

## 10. Változásnapló

| Dátum | Session | Mi történt |
|---|---|---|
| 2026-11-05 | #1 | PHP feltérképezése, javítása és lezárása (`master`), Go-s terv és kísérlet (`docs/`). |
| 2026-11-05 | #1 | Lezárás: fuzzerek (3×25 mp, hibátlan), S14 CSP valódi böngészőben (0 sértés, pozitív kontrollal), képernyőkép-ellenőrzés, valós adaton mért keresési késleltetés (p95 2,7 ms). |
| 2026-11-05 | #1 | **A Go program megépítése**: config, upstream kliens (böngésző UA, **http/https proxy**), képek, store (atomi publikálás, authorizer), ingest, web, Containerfile, Quadlet, e2e. Valós weekly: 962 műsor, 0 hiba. Tesztek: `go test -race` zöld, e2e 48/48, govulncheck tiszta. PHP eltávolítva az ágról. |
