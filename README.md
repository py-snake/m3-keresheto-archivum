# m3-keresheto-archivum

Az **M3** (Nemzeti Archívum) műsorainak kereshető archívuma. Egyetlen, statikus **Go** bináris, **SQLite** adatbázissal, podmanban futtatva.

* a műsorlistát és a borítóképeket időzített feladatok gyűjtik az M3 oldaláról,
* a webes felület csak olvas: egy keresőmező és a találati lista (JavaScript nélkül),
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

Élesben a [deploy/quadlet](deploy/quadlet) egységei (systemd + podman) futtatják a webet, az időzített gyűjtést és a Cloudflare Tunnelt.

## Parancsok

| Parancs | Mit csinál |
|---|---|
| `m3 serve` | a webszerver (csak olvasó szerep) |
| `m3 ingest daily` | a mai műsorlista és a (kb. 8 napos) műsorújság |
| `m3 ingest weekly` | a nyitott archívum gyűjteményei, a sorozatok minden része |
| `m3 ingest images` | csak a hiányzó borítóképek letöltése |
| `m3 ingest recheck` | a legrégebben ellenőrzött műsorok elérhetőségének újralekérdezése. **Alapból ki van kapcsolva**, lásd az Elérhetőség fejezetet |
| `m3 ingest migrate` | az adatbázis-séma frissítése (nem küld kérést az M3 felé; bármelyik gyűjtés is elvégzi) |
| `m3 ingest item M3-… [M3-…]` | adott műsorok felvétele azonosító alapján (rendszergazdai eszköz) |
| `m3 healthcheck [url]` | 0-val lép ki, ha a szerver válaszol |
| `m3 version` | verzió |

Kérésre felvétel a weben **nincs**: a webes oldalról semmi nem ír az adatbázisba.

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

## Biztonsági tulajdonságok

* A webszerep egyetlen statikus bináris `scratch` alapú image-ben: nincs shell, csomagkezelő, értelmező. Nem root felhasználóként, **csak olvasható** gyökér- és adat-fájlrendszerrel, minden Linux-képesség nélkül, **kimenő hálózat nélkül** fut.
* Az adatbázist csak olvasható és megváltoztathatatlan módban nyitja meg, **deny-by-default SQLite authorizer** mögött (SELECT-en kívül semmi: `ATTACH`, írás, `PRAGMA`, `sqlite_master` tiltott).
* Minden válaszon szigorú fejlécek: CSP (`default-src 'none'`, nincs script), `nosniff`, `Referrer-Policy: no-referrer`, HSTS és társaik. Nincs JavaScript, nincs süti, nincs külső betöltés.
* A felhasználói keresőszöveg sosem SQL vagy FTS-szintaxis, mindig kötött paraméter, idézőjelezve.
* A kimenő kliens egyetlen hosztra szól, az átirányítások is megkötöttek, minden válasz mérete és állapotkódja ellenőrzött, a sütiket eldobja. Az M3-tól jövő adat nem megbízható: ellenőrzött, megtisztított, és ha túl sok az érvénytelen elem, a futás **semmit nem publikál**.
* A borítóképeket a program dekódolja és újrakódolja (metaadat, hozzáfűzött adat, dekompressziós bomba kiszűrve), és `os.Root`-on át szolgálja ki, ami kizárja az útvonal-bejárást és a szimbolikus linkes kitörést.

A „feltörhetetlenség" nem garantálható; a terv és a maradó kockázatok a [docs/GO-DESIGN.md](docs/GO-DESIGN.md)-ben vannak.

## Tesztek és fejlesztés

```bash
go vet ./... && go test -race ./...        # egység- és integrációs tesztek
govulncheck ./...                           # ismert sebezhetőségek
go test -fuzz=FuzzParseProgram ./internal/upstream   # fuzz (példa)

podman build --target m3 -t localhost/m3:dev . && podman build --target e2e-proxy -t localhost/m3-e2e-proxy:dev .
tests/e2e/run.sh                            # végponttól végpontig, valódi M3 oldallal, izolált podman hálózatokon
```

Az `tests/e2e/run.sh` bizonyítja, hogy a proxyn át futó gyűjtés **minden** forgalma a proxyn megy ki: a gyűjtő konténer egy `--internal` hálózaton van, aminek nincs útja az internet felé.

## Licenc

AGPL-3.0, lásd [LICENSE.md](LICENSE.md). Az eredeti projekt: [a-sync/m3-keresheto-archivum](https://github.com/a-sync/m3-keresheto-archivum).
