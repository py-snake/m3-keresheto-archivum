# m3-keresheto-archivum

Az **M3** (Nemzeti Archívum) műsorainak kereshető archívuma. Egyetlen, statikus **Go** bináris, **SQLite** adatbázissal, podmanban futtatva.

* a műsorlistát és a borítóképeket időzített feladatok gyűjtik az M3 oldaláról,
* a webes felület csak olvas: keresés (részletes szűrőkkel és rendezéssel, gépelés közben is), találati lista, a JavaScript csak ehhez az opcionális gépelés közbeni kereséshez kell,
* az adatbázis egyetlen fájl a **gép lemezén**, így a felhasználó közvetlenül menthető.

Az eredeti PHP-s változat a `master` ágon van; ez az ág annak teljes kiváltása. A tervezés indoklása: [docs/GO-DESIGN.md](docs/GO-DESIGN.md).

## Gyors kezdés

```bash
podman build --target m3 -t m3:dev .

mkdir -p ~/m3/data
# adatgyűjtés (az első futás néhány percig tart; a képeket fokozatosan tölti le)
podman run --rm --read-only --cap-drop=ALL --userns=keep-id --user $(id -u):$(id -g) \
  -v ~/m3/data:/data:z m3:dev ingest weekly
podman run --rm --read-only --cap-drop=ALL --userns=keep-id --user $(id -u):$(id -g) \
  -v ~/m3/data:/data:z m3:dev ingest daily

# webszerver (csak olvasható gyökér és adat, nincs hálózati hozzáférés a kimenő irányba)
podman run -d --name m3-web -p 127.0.0.1:8080:8080 --read-only --cap-drop=ALL --security-opt no-new-privileges \
  --userns=keep-id --user $(id -u):$(id -g) -v ~/m3/data:/data:ro,z m3:dev serve
```

Ezután: <http://127.0.0.1:8080>

Élesben a [deploy/quadlet](deploy/quadlet) egységei (systemd + podman) futtatják a webet és az időzített gyűjtést; a kiszolgálás útja Cloudflare Tunnel → nginx → m3-web ([deploy/INSTALL.md](deploy/INSTALL.md), nginx-konfig: [deploy/nginx/m3.conf](deploy/nginx/m3.conf)).

## Parancsok

| Parancs | Mit csinál |
|---|---|
| `m3 serve` | a webszerver (csak olvasó szerep) |
| `m3 ingest daily` | a mai műsorlista és a (kb. 8 napos) műsorújság |
| `m3 ingest weekly` | a nyitott archívum gyűjteményei, a sorozatok minden része |
| `m3 ingest images` | csak a hiányzó borítóképek letöltése |
| `m3 ingest recheck` | a legrégebben ellenőrzött műsorok elérhetőségének újralekérdezése. **Alapból ki van kapcsolva**, lásd az Elérhetőség fejezetet |
| `m3 ingest refresh` | a legrégebben frissített műsorok leíró adatainak újralekérése és javítása. **Alapból ki van kapcsolva** (`M3_REFRESH_PER_RUN`) |
| `m3 export` | a letölthető fájlok (SQLite-dump, CSV, JSONL, séma) előállítása az `export/` könyvtárba, hálózat nélkül |
| `m3 ingest migrate` | az adatbázis-séma frissítése (nem küld kérést az M3 felé; bármelyik gyűjtés is elvégzi) |
| `m3 ingest item M3-… [M3-…]` | adott műsorok felvétele azonosító alapján (rendszergazdai eszköz) |
| `m3 healthcheck [url]` | 0-val lép ki, ha a szerver válaszol |
| `m3 version` | verzió |

Kérésre felvétel a weben **nincs**: a webes oldalról semmi nem ír az adatbázisba.

## Keresés

* **Egyszerű keresés:** ékezet- és kis-nagybetűfüggetlen részszó-keresés a cím, alcím, leírás, készítők, közreműködők, műfaj és adatlap mezőiben (legalább 3 karakter). **1–2 karakter csak a címben keres.** `M3-…` azonosítók (vesszővel) azonosító szerint keresnek.
* **Részletes keresés** (a „Részletes keresés" panel): külön mezők a címre, alcímre, leírásra, készítőkre és közreműködőkre (legalább 3 karakter), **műfaj** (a valós műfajok listája darabszámmal), **évtartomány**, „csak a kiemeltek", „csak a most elérhetők". A mezők együtt szűrnek (ÉS).
* **Rendezés:** a keresőmező alatt, mindig látható legördülő: legutóbb felvett (alap), legrégebbi, év szerint (újabb/régebbi elöl), cím szerint (A–Z / Z–A, az ékezeteket figyelmen kívül hagyva), megjelenés szerint. A választás azonnal érvényesül.
* **Lapozás:** a találatok oldalanként 20 elemet mutatnak (alap), a **„Találat / oldal"** legördülővel 10, 20, 50 vagy 100 választható. A lapozó kompakt (első, utolsó és a mostani körüli oldalak, „Előző" / „Következő"), alatta **„Ugrás a(z) N oldalra"** mező: ez JavaScript nélkül is működik (a szerver a `/?…&oldal=N` címet a megszokott `/N?…` címre irányítja át, a keresés minden szűrőjét megtartva). A listán túli oldalszám az utolsó oldalra visz.
* **Telefonon és szűk képernyőn** a találatok kártyák: kis bélyegkép a cím és az elérhetőség mellett, a leírás alatta teljes szélességben; a lapozó „Előző | 3 / 40 | Következő" alakú, nagy, ujjal könnyen eltalálható gombokkal. A kereső mező csak asztali gépen, egérrel kapja meg magától a kurzort, **a kezdőlapon**: telefonon nem nyílik meg magától a billentyűzet, és lapozáskor sem.
* **Gépelés közbeni keresés:** a találatok gépelés közben frissülnek (kb. 300 ms késleltetéssel, egyetlen kéréssel a gyors gépelésre). Ez egy opcionális kiegészítés: JavaScript nélkül az űrlap sima GET kéréssel ugyanazt tudja.
* A lenyitott részletek az összes tárolt mezőt, az elérhetőségi előzményt és a „Kiemelt" jelvényt mutatják; az oldal alján látszik a lekérdezés ideje.

## Letöltések

A `/export/` oldalon az archívum teljes adata letölthető, hogy bárki mentheti vagy más programban felhasználhatja. A fájlokat az `m3 export` parancs állítja elő (naponta egyszer, lásd a Quadlet időzítőt), a webszerep csak kiszolgálja őket egy **fix névlistából**.

| Fájl | Tartalom |
|---|---|
| `m3-archive.sqlite.gz` | a teljes SQLite adatbázis tömörítve (teljes szöveges indexszel és előzménnyel): használható mentés |
| `schema.sql` | az adatbázis sémája az oszlopok leírásával (magyarul és angolul) |
| `m3-programs.csv.gz` | minden műsor CSV-ben (UTF-8, vesszővel). A `=`, `+`, `-`, `@` jellel kezdődő szöveges cellák elé `'` kerül, hogy a táblázatkezelő ne értelmezze képletként (CSV-injekció ellen) |
| `m3-programs.jsonl.gz` | soronként egy JSON objektum, az API szerkezetével |
| `SHA256SUMS`, `manifest.json` | ellenőrző összegek, a generálás adatai (a manifest utolsóként készül el) |

```bash
sha256sum -c SHA256SUMS && gunzip -k m3-archive.sqlite.gz
sqlite3 m3-archive.sqlite 'SELECT title, year FROM programs ORDER BY year LIMIT 5;'
```

Az export ugyanazt a zárolást használja, mint a gyűjtés: közben nem publikálódik új adatbázis, és minden fájl átnevezéssel, egészben cserélődik. Az adatok az M3 oldaláról származnak; **a nyilvános közzététel jogi felelőssége a te döntésed** (a borítóképek nem részei a letöltésnek). A `M3_PUBLIC_EXPORT=0` kikapcsolja.

## API

Csak olvasható REST API (JSON), hogy más programok is használhassák az adatokat: a leírása a `/api/` oldalon, szabványos **OpenAPI 3.1** dokumentuma a `/api/v1/openapi.json` címen van (ebből kliens generálható). Hitelesítés nincs, bármely weboldal olvashatja (CORS).

| Végpont | Mit ad |
|---|---|
| `GET /api/v1/` | összegzés: műsorok száma, az adatbázis frissessége, korlátok |
| `GET /api/v1/programs` | keresés és lista (`q`, `title`, `subtitle`, `description`, `creators`, `contributors`, `genre`, `info`, `year_from`, `year_to`, `playable`, `highlighted`, `sort`, `page`, `per_page`) |
| `GET /api/v1/programs/{id}` | egy műsor az elérhetőségi előzménnyel |

```bash
curl 'http://127.0.0.1:8080/api/v1/programs?q=béke&playable=true&per_page=5'
```

Ugyanazok a szabályok és korlátok, mint a weben (legalább 3 karakter a mezőszűrőkben, legfeljebb 100 elem/oldal, legfeljebb 1000 oldal, látogatónkénti sebességkorlát). A hibák JSON-ban jönnek (`{"error":{"code":"…","message":"…"}}`), és soha nem tükrözik vissza a kérés tartalmát. Az `availability` az **utolsó megfigyelt** állapot (`checked_at`-tel): a lejátszhatóság időablakos, ezért változhatott. Sok adathoz ne az API-t olvasd végig, hanem töltsd le a teljes adatot.

## Elérhetőség: játszható-e egy műsor

Az M3 minden műsorhoz jelzi, hogy az adott pillanatban lejátszható-e (`playable`), és megadja a lejátszható napokat is. Ez **időablakos és változik**: ugyanaz a műsor lejárhat, majd egy későbbi ablakban újra elérhető lehet. A műsor azonosítójával a metaadat évekig lekérdezhető maradt, a lejátszás viszont ablakhoz kötött (és DRM-védett).

* A program az **utolsó megfigyelt** elérhetőséget tárolja, mellé az ellenőrzés időpontját és az utolsó változás idejét. A leíró adatok (cím, leírás…) továbbra is az **első látott** állapotot őrzik.
* A felületen jelvény mutatja: **Elérhető** / **Nem elérhető** / **Ismeretlen** (még nem ellenőriztük) / **Eltűnt az M3-ról** (az adatai itt megmaradnak). A jelvény fölé víve látszik, mikor ellenőriztük utoljára: az érték azóta változhatott. A „Csak a most elérhetők" jelölőnégyzet szűr rá, az „M3 oldal ↗" link a műsor saját oldalára visz (a nem elérhető műsor oldala is betölt, de lejátszó helyett „jelenleg nem elérhető" üzenetet mutat).
* **Az elérhetőség ingyen frissül**: a napi műsorlista és a műsorújság elemei, valamint az újonnan felvett műsorok lekérdezése amúgy is tartalmazza a jelzőt, ezekből nem megy ki extra kérés.
* A változásokról az `availability_log` tábla vezet előzményt (csak változáskor kerül bele sor), így látszik, mikor volt utoljára elérhető egy műsor.

**Az M3 oldalának kímélése.** A program szándékosan nem „botol":
* Minden gyűjtő folyamatnak **kemény kérés-plafonja** van (`M3_MAX_REQUESTS`); eléréskor megáll.
* Kérések között mindig van szünet (`M3_REQUEST_DELAY_MS`), a képek és a műsorok lekérdezése is költségkerettel történik.
* Az **`ingest recheck` alapból ki van kapcsolva**. Ha mégis kell, kis számmal kapcsold be, pl. `M3_RECHECK_PER_RUN=30` (naponta 30 kérés, 3 másodperces szünettel). A legrégebben ellenőrzött műsorokkal kezd, az eltűnteket nem kérdezi újra.
* A megfigyelés nélküli (`Ismeretlen`) műsorok addig azok maradnak, amíg a napi lista vagy a `recheck` nem érinti őket.
* Az **`ingest refresh` alapból ki van kapcsolva**: a leíró adatok az első látott állapotot őrzik, ezt csak kérésre javítja. Bekapcsolva kis keretű (`M3_REFRESH_PER_RUN`, lassú), a legrégebben frissítettel kezd, és **megtagadja a közzétételt, ha a válaszok sok műsor szövegét törölnék** (az M3 oldala romlott adatot adna); az eltűnt műsorokat megjelöli.

## Az adatkönyvtár (`M3_DATA_DIR`, alapból `/data`)

```
m3.sqlite                  a publikált adatbázis (mindig teljes és konzisztens)
images/M3-….jpg            a borítóképek (csak bővül)
backups/m3-ÉÉÉÉHHNN-….sqlite   a korábbi adatbázisok (alapból 14 db, keményhivatkozás: nem foglal többet)
```

* **Mentés:** elég a `m3.sqlite` (és az `images/`) másolása a gépen, bármikor, leállítás nélkül, mert a fájlt senki nem módosítja helyben: a gyűjtő egy másolaton dolgozik, ellenőrzi, majd **atomi átnevezéssel** cseréli. Egy megszakadt futás a korábbi adatbázist érintetlenül hagyja.
* Nincs régi (a PHP-s időkből származó) adat: az archívum a mai ablakból indul, és innentől gyűlik, ezért érdemes rendszeresen menteni.
* A keresés kis-nagybetű- és ékezetfüggetlen **részszó**-keresés (legalább 3 karakter), vagy `M3-…` azonosító(k) vesszővel elválasztva.

## Beállítások (környezeti változók)

| Változó | Jelentés | Alapérték |
|---|---|---|
| `M3_DATA_DIR` | az adatkönyvtár | `/data` |
| `M3_LISTEN` | a webszerver címe | `:8080` |
| `M3_TRUSTED_PROXIES` | azok a hálózatok (CIDR, vesszővel), amelyek `CF-Connecting-IP` / `X-Forwarded-For` fejlécét elhisszük | üres |
| `M3_RATE_LIMIT`, `M3_RATE_BURST` | látogatónkénti kérés/másodperc és kitörés | `3`, `15` |
| `M3_MAX_IN_FLIGHT` | egyidejű kérések plafonja | `64` |
| `M3_IMAGES_PER_RUN` | futásonként letöltött borítóképek száma | `300` |
| `M3_IMAGE_MAX_WIDTH` | a borítóképek átméretezése (px, 0 = eredeti) | `480` |
| `M3_BACKUPS_KEPT` | megőrzött korábbi adatbázisok | `14` |
| `M3_REQUEST_DELAY_MS` | szünet két kimenő kérés között | `250` |
| `M3_MAX_REQUESTS` | **kemény plafon**: legfeljebb ennyi kérést küld egy gyűjtő folyamat az M3 felé, utána leáll és a többit a következő futásra hagyja | `2000` |
| `M3_RECHECK_PER_RUN` | az `ingest recheck` futásonként ennyi műsort kérdez le újra (`0` = kikapcsolva, legfeljebb `500`) | `0` |
| `M3_RECHECK_DELAY_MS` | szünet két újraellenőrző kérés között (legalább `500`) | `3000` |
| `M3_REFRESH_PER_RUN` | az `ingest refresh` futásonként ennyi műsor leíró adatát kérdezi le újra és frissíti (`0` = kikapcsolva, legfeljebb `500`) | `0` |
| `M3_REFRESH_DELAY_MS` | szünet két frissítő kérés között (legalább `500`) | `3000` |
| `M3_PUBLIC_API` | a REST API be van-e kapcsolva (`0` = ki) | `1` |
| `M3_PUBLIC_EXPORT` | a nyilvános letöltések be vannak-e kapcsolva (`0` = ki) | `1` |
| `UPSTREAM_USER_AGENT` | a kimenő kérések User-Agentje | valódi böngésző (Chrome) |
| `UPSTREAM_PROXY` | minden kimenő forgalom ezen a proxyn megy át | nincs |
| `UPSTREAM_PROXY_INSECURE` | a **proxy** TLS-tanúsítványát nem ellenőrzi | kikapcsolva |
| `UPSTREAM_CA_FILE` | további megbízható gyökértanúsítványok (PEM) az M3 oldalához | nincs |

### Kimenő kérések proxyn át

Alapból a gyűjtő a gép saját IP-címéről éri el az M3 oldalát. Ha ezt nem szeretnéd, add meg a proxyt:

```bash
# HTTP proxy
UPSTREAM_PROXY=http://proxy.pelda.hu:3128
# HTTP proxy hitelesítéssel
UPSTREAM_PROXY=http://felhasznalo:jelszo@proxy.pelda.hu:3128
# HTTPS proxy (a kapcsolat a proxyhoz TLS-sel védett)
UPSTREAM_PROXY=https://proxy.pelda.hu:8443
# HTTPS proxy önaláírt tanúsítvánnyal: csak a PROXY tanúsítványát nem ellenőrzi
UPSTREAM_PROXY=https://proxy.pelda.hu:8443
UPSTREAM_PROXY_INSECURE=1
```

* A proxy `CONNECT` módszerrel nyit alagutat. A **céloldal (nemzetiarchivum.hu) tanúsítványát minden esetben ellenőrzi** a program, az „insecure" kapcsoló ezt nem gyengíti. Ha a proxy a forgalmat saját tanúsítvánnyal újraírja (TLS-lehallgatás), a proxy gyökértanúsítványát az `UPSTREAM_CA_FILE`-lal add meg, ne kikapcsolt ellenőrzéssel.
* A céloldal nevét a **proxy oldja fel**, a gép nem végez DNS-kérést az M3 felé.
* A proxy jelszava nem kerül a naplóba vagy hibaüzenetbe. Jelszót ne tégy a parancssorba; használj `podman secret`-et (lásd a Quadlet egységeket).
* A környezeti `HTTP_PROXY` / `HTTPS_PROXY` változókat a program **szándékosan nem használja**: csak az `UPSTREAM_PROXY` számít.
* Ha a proxy nem érhető el, a program **nem esik vissza közvetlen kapcsolatra**, hanem hibával leáll.

## Gyorsítótárazás

A weboldal a böngészőknek és egy esetleges CDN-nek (pl. Cloudflare) a következő élettartamokat adja a `Cache-Control` fejlécben, így a képeket és a stílusfájlokat az eredeti szerver gyakorlatilag csak egyszer szolgálja ki:

| Tartalom | Élettartam |
|---|---|
| Borítóképek (`/img/<azonosító>.jpg`) | **1 év**, `immutable` (a kép egyszer készül, és soha nem íródik felül) |
| Statikus fájlok a tartalom lenyomatával (`/static/app.css?v=<hash>`, `search.js`, `m3.min.css`, a logó, a helykitöltő) | **1 év**, `immutable`: ha a fájl tartalma változik, új címet kap |
| Statikus fájl hamis vagy hiányzó `v` értékkel | 1 nap |
| Találati oldalak (HTML) | 60 másodperc |
| Hiányzó kép vagy fájl (404) | nem tárolható (`no-store`) |

A helykitöltő és a saját borító **két külön cím** (`/static/placeholder.svg?v=…` és `/img/<azonosító>.jpg`), ezért egy hosszan tárolt helykitöltő nem maradhat bent olyan műsornál, amelyik közben saját borítót kapott: az oldal ilyenkor egyszerűen az új címre hivatkozik. Az oldal HTML-je rövid ideig él, így ez egy percen belül érvényesül.

A fejlécet az alkalmazás állítja, az nginx változtatás nélkül továbbadja. A tényleges gyorsítótár a böngésző és (ha van) a CDN, az nginx magában nem tárol. Cloudflare mellett a `.jpg`, `.css`, `.js`, `.svg` és `.png` fájlokat alapból tárolja, a HTML-t nem. Az „egyszer szolgálja ki" állítás közelítés: a CDN a ritkán kért elemeket az élettartam lejárta előtt is eldobhatja, és több adatközpont külön-külön kérhet (a *Tiered Cache* beállítás ezt csökkenti).

## Biztonsági tulajdonságok

* A webszerep egyetlen statikus bináris `scratch` alapú image-ben: nincs shell, csomagkezelő, értelmező. Nem root felhasználóként, **csak olvasható** gyökér- és adat-fájlrendszerrel, minden Linux-képesség nélkül, **kimenő hálózat nélkül** fut.
* Az adatbázist csak olvasható és megváltoztathatatlan módban nyitja meg, **deny-by-default SQLite authorizer** mögött (SELECT-en kívül semmi: `ATTACH`, írás, `PRAGMA`, `sqlite_master` tiltott).
* Minden válaszon szigorú fejlécek: CSP (`default-src 'none'`; egyetlen engedett script a saját `search.js`, nincs inline script, nincs külső betöltés), `nosniff`, `Referrer-Policy: no-referrer`, HSTS és társaik. Nincs JavaScript, nincs süti, nincs külső betöltés.
* A felhasználói keresőszöveg sosem SQL vagy FTS-szintaxis, mindig kötött paraméter, idézőjelezve.
* A kimenő kliens egyetlen hosztra szól, az átirányítások is megkötöttek, minden válasz mérete és állapotkódja ellenőrzött, a sütiket eldobja. Az M3-tól jövő adat nem megbízható: ellenőrzött, megtisztított, és ha túl sok az érvénytelen elem, a futás **semmit nem publikál**.
* A borítóképeket a program dekódolja és újrakódolja (metaadat, hozzáfűzött adat, dekompressziós bomba kiszűrve), és `os.Root`-on át szolgálja ki, ami kizárja az útvonal-bejárást és a szimbolikus linkes kitörést.

A „feltörhetetlenség" nem garantálható; a terv és a maradó kockázatok a [docs/GO-DESIGN.md](docs/GO-DESIGN.md)-ben vannak.

## Tesztek és fejlesztés

```bash
go vet ./... && go test -race ./...        # egység- és integrációs tesztek
govulncheck ./...                           # ismert sebezhetőségek
go test -run '^$' -fuzz='^FuzzSearchQueryFields$' -fuzztime=30s ./internal/store   # fuzzerek: keresés, API, feldolgozás, képek

podman build --target m3 -t localhost/m3:dev . && podman build --target e2e-proxy -t localhost/m3-e2e-proxy:dev .
tests/e2e/run.sh                            # végponttól végpontig, valódi M3 oldallal, izolált podman hálózatokon
python3 tests/e2e/browser.py http://127.0.0.1:8080   # valódi böngésző: a gépelés közbeni keresés, CSP-sértés nélkül
```

Az `tests/e2e/run.sh` bizonyítja, hogy a proxyn át futó gyűjtés **minden** forgalma a proxyn megy ki: a gyűjtő konténer egy `--internal` hálózaton van, aminek nincs útja az internet felé.

## Licenc

AGPL-3.0, lásd [LICENSE.md](LICENSE.md). Az eredeti projekt: [a-sync/m3-keresheto-archivum](https://github.com/a-sync/m3-keresheto-archivum).
