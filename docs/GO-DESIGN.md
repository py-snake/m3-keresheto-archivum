# Go-s teljes kiváltás: terv (`go` branch)

> **Állapot (2026-11-05): MEGVALÓSÍTVA.** A dokumentum a tervet és az indoklást őrzi; a tényleges állapotot és az eltéréseket a [18. fejezet](#18-megvalósítási-állapot-és-eltérések-a-tervtől) írja le. (Az eredeti, PHP-s `master` ág nem releváns tovább.)
> **Eldöntött (2026-11-05, a felhasználó):** **D1** nincs `/add`; **D3** a borítóképeket a Go bináris szolgálja ki helyben tárolt fájlokból (a statikus tartalom az exe-be fordítva); **D6** nincs régi adat, üres adatbázisról indulunk; **kifelé mindig valódi böngésző User-Agent**.
> **Elsődleges szempont: a biztonság.** Minden *mért* tény mellett dátum és bizonyíték áll; ami nincs megmérve, az `(terv)` vagy `(ellenőrizendő)` jelölést kap.
> A dokumentum élő: a döntések és mérések változásakor frissítsd, és a [CLAUDE.md](../CLAUDE.md) változásnaplójába is írj egy sort.

---

## 1. Cél és nem-célok

**Cél:** a PHP/CodeIgniter alkalmazás (kereshető M3-archívum) funkcionális kiváltása **egyetlen statikus Go binárissal**, SQLite adatbázissal, podmanban futtatva, Cloudflare Tunnelen át kiszolgálva. Az SQLite fájl a **hoszt lemezén** van, hogy a hoszt felhasználó közvetlenül mentheti.

**Nem-célok:** új funkciók (kártya layout, rádió műsorok) az első kiadásban; felhasználói fiókok, bejelentkezés, bármilyen webes írási út; **kérésre felvétel (`/add`)**: a felhasználó döntése szerint nem kell.

## 2. Mit jelent itt a „feltörhetetlen"?

Őszintén: **feltörhetetlen rendszer nem létezik**, és ezt senki nem tudja garantálni. Amit **tervezéssel megígérhetünk és tesztelhetünk**:

1. **Nincs mit ellopni:** a rendszerben nincs jelszó, API-kulcs, felhasználói adat. Az adatbázis eleve nyilvános adat. (Az egyetlen titok a Cloudflare tunnel token, ami külön konténerben él.)
2. **Nincs webes írási út:** az interneten át elérhető folyamat nem tud írni semmit (csak olvasható mount, authorizerrel zárt SQLite-kapcsolat).
3. **Nincs mit futtatni:** a web konténer egyetlen statikus bináris (scratch image: nincs shell, nincs interpreter, nincs csomagkezelő, nincs OS-csomag).
4. **Nincs hová kapcsolódni:** a web konténernek nincs kimenő hálózata.
5. **Korlátos kár:** ha mégis hiba van a kódban, a támadó egy csak olvasható, hálózat nélküli, titokmentes, nem-root konténerben kap kódfuttatást, ahol nincs mire.
6. **Visszaállítható:** az adat bármikor visszaállítható a mentésekből.

**Maradó kockázatok (elfogadott, nyíltan):** hiba a Go standard könyvtárban / az SQLite-ban / a függő modulokban (ellenszer: `govulncheck`, gyors újraépítés); a Cloudflare-fiók vagy a tunnel token kompromittálása; a hoszt vagy a kernel kompromittálása (rootless podman + seccomp csökkenti, nem szünteti meg); DoS (a Cloudflare enyhíti); az upstream (M3) adatának megmérgezése (csak szövegként jelenik meg, nincs végrehajtás; a nem megbízható adat elfogadási szabályai a [9. fejezetben](#9-az-adatgyűjtő-ingest)). Függetlenül végzett biztonsági felülvizsgálat/pentest **javasolt** az élesítés előtt.

## 3. Fenyegetésmodell

| Eszköz (asset) | Miért érték | Védelem |
|---|---|---|
| Az SQLite fájl épsége | az archívum a termék; a régi adat nem pótolható | a web csak olvasó; az ingest atomi publikál + mentések rotálása + épségellenőrzés |
| A szolgáltatás elérhetősége | | Cloudflare, rate limit, párhuzamosság-korlát, lekérdezés-időkorlát |
| A hoszt és a hálózat | egy feltört konténerből továbblépés | rootless, cap-drop, no-new-privileges, seccomp, ro rootfs, nincs kimenő hálózat |
| A látogatók | XSS, követés | nincs JS, szigorú CSP, nincs sütik/session, nincs naplózott nyers IP |
| Cloudflare tunnel token | a tunnel átvétele | `podman secret`, csak a cloudflared konténerben |

| Támadó | Képesség | Mit nem tud (a terv szerint) |
|---|---|---|
| Névtelen internetes támadó | tetszőleges HTTP kérés | írni, fájlt olvasni a lemezről, SQL-t futtatni, kimenő kapcsolatot indítani |
| Rosszindulatú/kompromittált **upstream** (M3) | tetszőleges JSON/HTML válasz az ingestnek | kódot futtatni, az adatbázist félig írni (a validáció + atomi publikálás miatt), a weben szkriptet futtatni (a kimenet escape-elt, nincs JS) |
| Ellátási lánc (függőségek) | rosszindulatú modulverzió | észrevétlenül bekerülni (pin + `go.sum` + `govulncheck`, 4 beépülő modul) |
| Helyi támadó a hoszton | | (a hoszt védelme nem e projekt hatóköre) |

## 4. PHP → Go megfeleltetés

| PHP (`master`) | Go | Megjegyzés |
|---|---|---|
| `App::index` keresés + lapozás (10/oldal), `?kereses=` | `GET /` és `GET /{n}` | ugyanaz az URL-szerkezet, hogy a régi linkek éljenek |
| `?nyers` nyers nézet | `GET /?nyers` | marad (nyilvános adat) |
| Keresés: `LIKE '%x%'` 8 oszlopon + `program_id` | FTS5 trigram ([7. fejezet](#7-keresés)) | gyorsabb; a szemantikát valós adaton össze kell vetni |
| `M3-…` azonosító(k) keresése | pontos egyezés a `program_id` indexen | ≤50 azonosító |
| `Cron::daily/weekly` | `m3 ingest daily` / `m3 ingest weekly` | külön konténer/timer |
| `Cron::backup` (`m3-db.gz`) | a publikált SQLite fájl **maga a mentés** + `m3 backup` (rotáció) | nincs külön dump |
| `Cron::csv` (`m3-db.csv.gz`) | `m3 export csv` (opcionális, [16. fejezet](#16-nyitott-döntések-a-felhasználónak)) | CSV-injection ellen escape (SEC-12) |
| `Cron::refresh` | `m3 ingest refresh <from_id>` | helyben frissít, megtartja `added_at`-et |
| `POST /add` | **nincs, véglegesen** (D1); a hoszton futtatható `m3 ingest item M3-…` adminisztrációs eszközként megmarad | a web így tényleg csak olvasó |
| `.htaccess` / nginx allowlist | nincs rá szükség: a Go szerver csak a saját útvonalait ismeri | |
| `m3.min.css` + `app.css` | beágyazott (`embed`) statikus fájlok | a Google Fonts/unpkg helyett **saját tárolású** betű/ikon (CSP + adatvédelem) |
| Inline JS (kereső UX, `/add` hívás) | **nincs JS** | a keresés sima GET űrlap; `script-src 'none'` |
| Borítókép: `<img src="https://nemzetiarchivum.hu/images/m3/ID">` | `GET /img/{program_id}.jpg` **helyi fájlból** (D3); az ingest tölti le és dolgozza fel ([9.1](#91-borítóképek)) | az M3 nem látja a látogatókat; `img-src 'self'` |

A PHP-oldali hibaosztályok (SEC-1…14, BUG-1…17, lásd [CLAUDE.md](../CLAUDE.md)) mindegyikéhez a Go-s terv **szerkezeti** választ ad: a paraméterezett SQL kötelező, nincs írható web, az upstream kliens státuszt és sémát ellenőriz, a nulla-értékek típusrendszerrel kezeltek, az idők UTC-ben egész számként tároltak, a kapcsolat utf-8.

## 5. Architektúra

**Egy bináris (`m3`), két futási szerep, különböző jogosultsággal:**

```
                        Internet
                           │ (HTTPS, Cloudflare WAF / rate limit / cache)
                   ┌───────▼────────┐
                   │   cloudflared  │  két hálózaton: m3-internal + alapértelmezett (kimenő)
                   └───────┬────────┘  token: podman secret
                           │ HTTP (privát hálózat)
              ┌────────────▼─────────────┐
              │  m3 serve  (web szerep)  │  m3-internal (internal=true: NINCS kimenő hálózat)
              │  scratch · ro rootfs     │  /data :ro  ← csak olvas
              │  nincs cap · nem root    │
              └────────────┬─────────────┘
                           │ olvas (immutable=1, authorizer)
        ┌──────────────────▼───────────────────┐
        │  HOSZT: /srv/m3/data/                │  a hoszt felhasználó tulajdona
        │    m3.sqlite        (publikált)      │  ← a hoszt user ezt menti (mindig konzisztens)
        │    backups/m3-YYYYMMDD.sqlite (N db) │
        │    images/M3-….jpg   (csak bővül)    │
        └──────────────────▲───────────────────┘
                           │ ír: m3.sqlite.new → rename (atomi)
              ┌────────────┴─────────────┐
              │ m3 ingest (gyűjtő szerep)│  timer által indított egyszeri konténer
              │ scratch · ro rootfs      │  /data :rw · kimenő hálózat
              │ nincs cap · nem root     │  nincs bejövő port, nincs titok
              └──────────────────────────┘
```

### 5.1 Az „atomi publikálás" invariáns (a terv gerince)

- A **publikált fájlt soha, senki nem módosítja helyben.** Az ingest egy **másolaton** dolgozik (`m3.sqlite.new`), ellenőrzi, majd `rename`-mel cseréli. Emiatt:
  - a web **WAL nélkül, `immutable=1`-gyel, csak olvashatóan** nyithatja meg (nincs `-wal`/`-shm` melléktermék, nincs zárolás, nincs írási jog);
  - a hoszt felhasználó **bármikor másolhatja** a fájlt, mindig konzisztens (nem kell leállítani, nem kell `VACUUM INTO`).
- A web szerep a cseréről az inode/mtime periodikus (pl. 30 s) `stat`-jából értesül, új kapcsolatot nyit, a régit türelmi idő után zárja. *(Mérve: a régi `immutable` kapcsolat a régi pillanatképet látja, az újra nyitott az újat.)*
- **Ingest lépések:** `flock` (nincs két párhuzamos futás) → másolat → módosítás tranzakcióban → `PRAGMA integrity_check`, `foreign_key_check`, FTS5 `integrity-check` → **ésszerűségi ellenőrzés** (a sorok száma nem csökkenhet, az új sorok száma korlát alatt) → `fsync` fájl és könyvtár → a régi fájl **hardlinkje** `backups/`-ba, rotáció → `rename`.
- Hibánál a `.new` fájl törlődik, a publikált fájl érintetlen marad, a kilépési kód ≠ 0.
- **Képek:** az `images/` könyvtár **csak bővül**: a fájlokat az ingest `tmp` névre írja, `fsync`, majd `rename`; létező fájlt nem módosít. A képek az adatbázis **publikálása előtt** készülnek el, így a publikált adatbázis soha nem hivatkozik hiányzó képre. A web ugyanazt a könyvtárat csak olvashatóan látja.

### 5.2 Hoszt-oldali elrendezés és jogosultság

- `/srv/m3/data/` a **hoszt felhasználóé**; a konténerek `--userns=keep-id` módban futnak, így a fájlok tulajdonosa a hoszt felhasználó marad, és `sudo` nélkül menthetők. *(Mérve: a konténerből írt/cserélt fájl `multibox:multibox` tulajdonú maradt.)*
- A web mountja `:ro`; az ingesté `:rw`. A mentés a hoszt dolga: `cp`/`rsync`/`restic` a `m3.sqlite`-ra (vagy a `backups/` könyvtárra).

## 6. Adatmodell (SQLite)

A PHP `programs` tábla portja, **szigorú típusokkal** és védő megszorításokkal (a hosszkorlátok a PHP-s varchar-méretek):

```sql
CREATE TABLE programs (
  id            INTEGER PRIMARY KEY,
  program_id    TEXT NOT NULL UNIQUE CHECK (length(program_id) BETWEEN 4 AND 31),
  title         TEXT NOT NULL CHECK (length(title) <= 255),
  subtitle      TEXT NOT NULL DEFAULT '' CHECK (length(subtitle) <= 255),
  info          TEXT NOT NULL DEFAULT '',  extended_info TEXT NOT NULL DEFAULT '',
  description   TEXT NOT NULL DEFAULT '',  short_description TEXT NOT NULL DEFAULT '',
  company       TEXT NOT NULL DEFAULT '',
  year          INTEGER NOT NULL DEFAULT 0 CHECK (year BETWEEN 0 AND 2100),
  country       TEXT NOT NULL DEFAULT '' CHECK (length(country) <= 3),
  creators TEXT NOT NULL DEFAULT '', contributors TEXT NOT NULL DEFAULT '', genre TEXT NOT NULL DEFAULT '',
  quality TEXT NOT NULL DEFAULT '', pg TEXT NOT NULL DEFAULT '', ratio TEXT NOT NULL DEFAULT '',
  duration      TEXT NOT NULL DEFAULT '',   -- HH:MM:SS:FF (a FF képkocka)
  has_subtitle  INTEGER NOT NULL DEFAULT 0 CHECK (has_subtitle IN (0,1)),
  is_series     INTEGER NOT NULL DEFAULT 0 CHECK (is_series IN (0,1)),
  series_id     TEXT NOT NULL DEFAULT '',   -- dekódolt (a PHP is így tárolta)
  episode       INTEGER NOT NULL DEFAULT 0, episodes INTEGER NOT NULL DEFAULT 0,
  added_at      INTEGER NOT NULL,           -- unix, UTC
  released_at   INTEGER NOT NULL DEFAULT 0, -- unix, UTC; 0 = ismeretlen
  image_state   INTEGER NOT NULL DEFAULT 0 CHECK (image_state IN (0,1,2)) -- 0 még nincs, 1 van helyi kép, 2 az upstreamnek nincs képe
) STRICT;
```

- **Idők:** egész szám, UTC (a PHP `released` datetime fixen Budapest-idő volt, a korábbi „-1 óra" trükkel). A megjelenítés `Europe/Budapest`-ben; a scratch image-be a `time/tzdata` csomagot **be kell fordítani** (`import _ "time/tzdata"`).
- **A `released` jelentése** az upstream `start_playable_dts` + `start_startTime_dts` **minimuma**, mint a PHP-ban (BUG-7: jelentése továbbra is vitatható; paritás első körben).
- **Új, nem tárolt upstream mezők** (a PHP nem tárolta): `collection`, `isHighlighted`, `playable`, a teljes lejátszási ablakok. Később, külön döntés.
- **Nincs régi adat (D6):** üres adatbázisról indulunk, a mai (és a következő ~8 napos) ablakból; nincs `m3 import`. A történet innentől gyűlik, ezért a **mentés kiemelten fontos** (a lemez a hoszt felhasználó felelőssége, [5.2](#52-hoszt-oldali-elrendezés-és-jogosultság)).

## 7. Keresés

**Mért (2026-11-05):** a `ncruces/go-sqlite3` driverrel az SQLite 3.54.1 FTS5-je működik `tokenize='trigram case_sensitive 0 remove_diacritics 1'` beállítással.

- `bék`, `BÉK`, `bek` **ugyanazt** találta (kis-nagybetű- és ékezetfüggetlen **részszó**-keresés, mint a PHP `LIKE %x%`-e, de indexelt).
- **3 karakternél rövidebb keresés nem talál** (trigram korlát) → **szabály:** a szöveges keresés legalább 3 karakter; rövidebbre a UI üzenetet ad („Legalább 3 karakter"). Az `M3-…` azonosítós keresés pontos egyezés, erre nem vonatkozik.
- **A felhasználói szöveg SOHA nem FTS5-szintaxis:** mindig egyetlen, idézőjelezett kifejezésként megy be (`"` → `""`). *(Mérve: `" OR title:* NEAR(a b) AND NOT ^x`, `'; DROP TABLE …`, `*` mind szó szerinti szövegként viselkedett, 0 találat, nincs hiba.)*
- **Korlátok (terv):** a keresőszó ≤100 karakter (rune), találatszám-plafon (`SELECT count(*) FROM (… LIMIT 1001)` → „1000+ találat"), oldalszám ≤100, lekérdezés-időkorlát (`context` → SQLite interrupt).
- **Indexelt oszlopok:** `title, subtitle, info, extended_info, description, creators, contributors, genre` (paritás). **Méret:** a trigram-index a mérésben ≈3,5-szerese a szövegnek (425 MB / 100 000 szintetikus sor). **A valós adaton újra kell mérni**, és szükség esetén az `info`/`extended_info` kivehető.
- **Teljesítmény (terv-kapu):** a szintetikus, szinte minden sort találó (98%) korpuszon egy keresés 200–400 ms volt (legrosszabb eset, a SQLite WASM-fordításban). A valós, szelektívebb adaton várhatóan jóval gyorsabb, de **a fázis-0 elfogadási kritérium a valós adaton mért p95 lekérdezési idő** ([15. fejezet](#15-fázisok)).

## 8. A HTTP réteg (`m3 serve`)

- **Csak a standard könyvtár** (`net/http`, `html/template`, `embed`), külső router/keretrendszer nélkül. Útvonalak: `GET /`, `GET /{n}`, `GET /public/…` (beágyazott fájlok), `GET /img/{program_id}.jpg` (helyi borítókép, [9.1](#91-borítóképek)), `GET /healthz`. Minden más **404**; `GET`/`HEAD` kivételével minden metódus **405**. Nincs kérés-törzs, nincs süti, nincs session, nincs állapot.
- **Szerver korlátok (terv):** `ReadHeaderTimeout` 5 s, `ReadTimeout` 10 s, `WriteTimeout` 15 s, `IdleTimeout` 30 s, `MaxHeaderBytes` 8 KB, URL ≤ 2 KB; egyidejű kérések plafonja (semaphore), túllépésnél 503.
- **Biztonsági fejlécek minden válaszon** (404/405/429/500 is):
  ```
  Content-Security-Policy: default-src 'none'; style-src 'self'; font-src 'self'; img-src 'self';
                           form-action 'self'; base-uri 'none'; frame-ancestors 'none'; object-src 'none'
  X-Content-Type-Options: nosniff        Referrer-Policy: no-referrer
  Permissions-Policy: camera=(), microphone=(), geolocation=(), interest-cohort=()
  Cross-Origin-Opener-Policy: same-origin  Cross-Origin-Resource-Policy: same-origin
  Strict-Transport-Security: max-age=31536000; includeSubDomains   (a Cloudflare is adja)
  ```
  Mivel **nincs JS**, a `script-src` nincs engedve (`default-src 'none'`). Az inline stílus sem kell (`style-src 'self'`). A CSP érvényesítését **valódi böngészőben** ellenőrizni kell (a helyi gépen van `chromium-browser` és `firefox`).
- **Kimenet:** kizárólag `html/template` (kontextusfüggő escape); a nem megbízható szöveg sehol nem kerül `template.HTML`-be.
- **Kliens-IP:** a közvetlen peer cím az alapértelmezett. A `CF-Connecting-IP`-t **csak akkor** fogadjuk el, ha a peer a megbízható hálózatban van (`TRUSTED_PROXIES`); ellenkező esetben a fejlécet figyelmen kívül hagyjuk (hamisítás ellen).
- **Rate limit:** IP-nkénti token-bucket memóriában, **korlátos méretű** (LRU), plusz globális párhuzamosság-plafon; 429 + `Retry-After`. A Cloudflare WAF/rate limiting az első védvonal.
- **Naplózás:** `log/slog` JSON a stdout-ra; **nyers IP nincs** (napi sózott HMAC-hash), a kereső szöveg nem naplózott.
- **Gyorsítótár:** a statikus fájlok hosszan cache-elhetők; a HTML rövid ideig (`public, max-age=60` jelölt; végleges érték: [16. fejezet](#16-nyitott-döntések-a-felhasználónak)).

## 9. Az adatgyűjtő (`m3 ingest`)

A nem megbízható bemenet **az upstream**. Szabályok:

- **Kimenő kapcsolat csak `https://nemzetiarchivum.hu`-ra:** a kliens hoszt-allowlistet kényszerít, **átirányításnál is** (`CheckRedirect`), TLS ≥ 1.2, a hitelesítés be van kapcsolva, kapcsolat 10 s / összes 60 s időkorlát, válasz-méret plafon (pl. 32 MiB, `io.LimitReader`), nem 2xx → hiba. **Valódi böngésző `User-Agent`** (felhasználói döntés; a PHP-ban `UPSTREAM_USER_AGENT`, a Go-ban is konfigurálható; a verziót karbantartani kell: egy évekkel ezelőtti Chrome feltűnő). Ugyanez vonatkozik minden más kifelé menő kérésre, így a képletöltésre is.
- **Végpontok** (a PHP-val azonos, [CLAUDE.md 6. fejezet](../CLAUDE.md)): `daily_program`, `program_guide?days=30`, `get-open`, `get-open?collection=`, `open?series=&page=` (HTML), `item?id=`. Az ismeretlen azonosító `200 + null` → „nincs ilyen elem" (nem hiba, nem összeomlás).
- **Validáció minden elemre:** `program_id` `^M3-[A-Z0-9]{1,27}$`, kötelező `title`; típus-ellenőrzés (a `subtitle` lehet `false`, tömbök csak skalárokat tartalmazhatnak); **hosszkorlátok** a sémával egyezően (a csonkolás explicit és naplózott); **vezérlő- és bidi-felülíró karakterek eltávolítása** (U+0000–001F, U+007F–009F, U+202A–202E, U+2066–2069), UTF-8 érvényesség, NFC.
- **Hibatűrés / áramkör-megszakító:** ha az érvénytelen elemek aránya meghalad egy küszöböt (pl. 5%), vagy a séma lényegesen eltér, a futás **leáll, semmi sem publikálódik**, nem-nulla kilépési kóddal. A **kanári**: ha egy sorozatoldal 0 azonosítót ad, az „elrendezés megváltozott" hiba, nem „üres sorozat".
- **Korlátok:** sorozat-lapozás ≤100 oldal, futásonkénti kérés-költségkeret, kérések közti szünet, `flock`.
- **A HTML-kivágás** (sorozatoldal): jelölőkeresés a PHP `extract_ids`-hez hasonlóan, **rögzített valós mintákon (golden fixture)** tesztelve; a lapozólink kódolása a PHP `urlencode`-dal egyezik, `page=0 ≡ page=1`, az utolsó utáni oldal üres *(mérve, UP-8)*.
- **Időzítés:** daily 8 óránként, weekly naponta (mint a PHP-ban); a systemd timer indítja, nincs HTTP-s cron.

### 9.1 Borítóképek

**Eldöntve (D3):** a képeket a Go bináris szolgálja ki, **helyben tárolt fájlból**. A statikus tartalom (CSS, logó, betűtípus, helyettesítő kép) az exe-be fordítva (`embed`). **A borítóképek az exe-be nem fordíthatók**, mert az ingest naponta újakat tölt le, az exe viszont build-időben készül: ezért a hoszt `data/images/` könyvtárába kerülnek (az adatbázis mellé, ugyanúgy a hoszt felhasználó menti).

**Mért (2026-11-05, 5 minta + 1 ismeretlen azonosító):**
- URL: `https://nemzetiarchivum.hu/images/m3/<program_id>`, `image/jpeg`, 8 058 – 40 747 bájt (átlag ≈ 24,5 KB), 280×210 – 720×608 px, `Cache-Control: max-age=86400`.
- **Ismeretlen azonosítóra is `HTTP 200`**, 393 bájtos `text/html` (egy „nem található” HTML-darab), **nem kép** → a státuszkód nem elég, a tartalmat **ellenőrizni kell**.
- A válasz sütit is állít (`Set-Cookie: MtvaArchivumToken=deleted…`); az ingest kliens **nem tárol sütit**.
- Tárolás-becslés (extrapoláció, nem mért nagy mintán): ~24,5 KB × 50 000 program ≈ 1,2 GB átméretezés nélkül.

**Letöltés és feldolgozás (terv):**
1. Csak érvényes `program_id`-ra, a hoszt-allowlisten át, böngésző User-Agenttel; kérések között szünet; **futásonkénti képkeret** (pl. legfeljebb 300 kép, legújabb elöl), így a kezdeti feltöltés fokozatosan történik.
2. **Ellenőrzés:** méretplafon (pl. 2 MiB, `io.LimitReader`); a tartalom első bájtjai **JPEG-et** jelezzenek (`FF D8 FF`), nem a `Content-Type` fejléc; `image.DecodeConfig` a **méretek** ellenőrzésére a dekódolás előtt (pl. oldalanként ≤ 4096 px, ≤ 12 megapixel: tömörítési-bomba ellen).
3. **Megtisztítás:** a standard `image/jpeg` memóriabiztos dekóderével **dekódolás, majd újrakódolás** (kiesik az EXIF/megjegyzés, a fájl végére fűzött adat, a poliglot-trükk). Opcionális átméretezés (pl. ≤ 480 px széles): a standard könyvtárban nincs átméretező, ehhez egyszerű dobozszűrőt kell írni (D12).
4. Írás `images/<program_id>.jpg.tmp` → `fsync` → `rename`. Siker: `image_state = 1`. Ha az upstream a „nem található” HTML-t adja: `image_state = 2` (később, pl. 30 nap múlva újrapróbálható). Átmeneti hiba: `image_state` marad 0, a következő futás újrapróbálja; **képhiba sosem állítja meg a futást**.

**Kiszolgálás:** `GET /img/{program_id}.jpg`: az azonosító szigorú szabályos kifejezéssel validált; a fájl megnyitása **`os.OpenRoot`**-tal (Go ≥ 1.24, helyi: 1.26.9), amely **kizárja az útvonal-bejárást és a szimbolikus linkes kitörést**; `http.ServeContent`; a `Content-Type` **rögzítetten** `image/jpeg`, `nosniff`; `Cache-Control: public, max-age=86400`. Ha nincs kép (`image_state ≠ 1` vagy hiányzik a fájl), a beágyazott helyettesítő kép jön. A weben így **nincs kimenő kérés**, az `img-src 'self'`.

**Jogi megjegyzés (a felhasználó döntése, D13):** a helyi tárolás a harmadik fél borítóképeinek **másolata**, nem csak hivatkozás. Ez szerzői jogi/ToS szempontból több, mint a hotlinkelés; a döntés és a felelősség a felhasználóé (SEC-10 kiterjesztése).

## 10. SQLite keményítés

**Driver (mért, 2026-11-05):** `github.com/ncruces/go-sqlite3` v0.36.0 (SQLite **3.54.1**, `database/sql` driver). A SQLite egy WASM-build **gépi fordítása Go forráskóddá** (`wasm2go`; ~222 000 sor generált Go), **CGO nélkül**: a memóriahibák a WASM lineáris memóriára korlátozódnak, nincs C kód a folyamatban. A **ténylegesen beépülő modulok száma 4**: `go-sqlite3`, `go-sqlite3-wasm`, `julianday`, `golang.org/x/sys`. A fordítási beállítások biztonsági szempontból kedvezők: `SQLITE_DQS=0`, `SQLITE_TRUSTED_SCHEMA=0`, `SQLITE_ENABLE_API_ARMOR`, **nincs extension-betöltés** (`load_extension` nem létezik), nincs deserialize.
- **Kockázat:** a fordítást egyetlen szerző végzi; a generált kód nem auditálható kézzel. Enyhítés: pin + `go.sum`, `govulncheck`, a **tároló réteg egy szűk interfész mögött** (`internal/store`), így a driver (pl. `modernc.org/sqlite`) cserélhető. A `modernc` változatot **nem mértük**; csere esetén újra kell futtatni a kísérletet.
- **FTS5** a kapcsolatonként regisztrálandó (`fts5.Register`), nem import-mellékhatás.

**A csak olvasható mód NEM elég (mért lelet):** `mode=ro&immutable=1` mellett az `ATTACH DATABASE '/…/evil.db'` **hiba nélkül lefutott** a web kapcsolaton. Ezért a web kapcsolat **deny-by-default SQLite authorizert** kap (bizonyítva a `docs/spikes/sqlite-hardening.go.txt` kísérletben):

| Engedett (mért) | Tiltott (mért: mind `authorization denied`) |
|---|---|
| `SELECT`; `READ` csak az alkalmazás táblákra (`programs`, `programs_fts`, `programs_fts_*` árnyéktáblák); `FUNCTION`: `match`, `count`, `lower`, `upper`, `min`, `max`, `coalesce`, `length`, `substr` (+ a ténylegesen szükséges `bm25`/`snippet`, ha használjuk); `PRAGMA query_only` (olvasás/bekapcsolás), `PRAGMA data_version` (olvasás, az FTS5 belső használja) | `ATTACH`, minden DML/DDL, minden más `PRAGMA` (pl. `journal_mode`), `sqlite_master` olvasása, ismeretlen függvény |

További: `PRAGMA query_only=1`, `trusted_schema=0`, `cell_size_check=1`, `mmap_size=0`; a kapcsolatpool kicsi; **soha nem épül SQL szövegből felhasználói bemenettel** (csak `?` paraméter). Az authorizer **kiegészítés**, nem helyettesíti a paraméterezést.

## 11. Konténer és üzemeltetés

**Image:** `FROM scratch` + a statikus bináris + CA-tanúsítványcsomag (az ingest TLS-éhez; a webnek nem kell) + tzdata (beforgatva). **Mért:** a kísérleti image **9,06 MB**, a bináris statikus (`CGO_ENABLED=0`, `-trimpath -ldflags='-s -w'`), `USER` nem-root.

**Futtatás (mérve a web szerepre, kísérleti binárissal):**
```
podman run --rm --network none --read-only --cap-drop=ALL --security-opt no-new-privileges \
           --userns=keep-id --user $(id -u):$(id -g) -v /srv/m3/data:/data:ro  m3  serve
```
A kísérlet ezzel a beállítással lefutott: a csak olvasható mountról olvasott, írási kísérlet hibát adott, melléktermék fájl (`-wal`/`-shm`) nem keletkezett.

**Quadlet (rootless, systemd) – vázlat, a fázis 4-ben kipróbálandó:**

| Egység | Hálózat | Mount | Megjegyzés |
|---|---|---|---|
| `m3-web.container` | `m3-internal` (`Internal=true`, **nincs kimenő forgalom**) | `/srv/m3/data:/data:ro` | nincs publikált port; healthcheck a bináris `healthcheck` alparancsával (scratch: nincs curl); `Memory`, `PidsLimit` korlát |
| `m3-cloudflared.container` | `m3-internal` + alapértelmezett (kimenő) | – | `Secret=` a tunnel tokennek; origin: `http://m3-web:8080` |
| `m3-ingest@daily/weekly.service` + `.timer` | alapértelmezett (kimenő), **nincs** kapcsolat a webes hálózattal | `/srv/m3/data:/data:rw` | egyszeri konténer, `ReadOnly=true`, cap-drop, időkorlát |

- **Titok:** a rendszerben egyetlen titok van, a tunnel token (`podman secret`), nem a repóban.
- **Kimenő forgalom szűrése az ingestnél:** első körben az alkalmazásbeli hoszt-allowlist ([9. fejezet](#9-az-adatgyűjtő-ingest)); később opcionális: allowlistes forward proxy vagy nftables.
- **Nincs adminer, nincs DB-szerver, nincs nyitott port** a hoszton (a cloudflared csak kifelé kapcsolódik).
- **Frissítés:** a base image nincs (scratch); a bináris újraépítése Go/modul-frissítéskor.
- **Mentés:** a hoszt felhasználó másolja a `/srv/m3/data/m3.sqlite`-ot (mindig konzisztens, [5.1](#51-az-atomi-publikálás-invariáns-a-terv-gerince)); az ingest emellett `backups/`-ba rotál.

## 12. Ellátási lánc és build

- Go **1.26.x** (helyi: 1.26.9) rögzítve a `go.mod` `toolchain` sorában; build konténerben, **digestre rögzített** `golang` image-ből.
- `go.sum` + `GOFLAGS=-mod=readonly`, `go mod verify`; **minden módosítás előtt a függőségi lista felülvizsgálata** (jelenleg 4 beépülő modul; a bővítés indoklást igényel). A standard könyvtár az elsődleges eszköz.
- CI/helyi kapuk: `go vet`, `staticcheck`, `go test -race`, **`govulncheck ./...`**, rövid fuzz-futások, `go build` reprodukálhatóság (`-trimpath`, `-buildvcs`), SBOM (`go version -m`).
- A generált SQLite-fordítás nagy (~6 MB forrás) → **nem vendorolunk**; helyette a pinelt verzió + `go.sum` + a modul-proxy ellenőrzőösszeg-adatbázisa.

## 13. Tesztelés és biztonsági elfogadási kritériumok

A kiadás feltétele, hogy **mind** teljesüljön (mindegyik automatizálható):

| # | Kritérium | Hogyan |
|---|---|---|
| S1 | `govulncheck` tiszta | CI |
| S2 | A bináris statikus, `cgo` nélküli | `file`/`ldd`, `go version -m` |
| S3 | A web szerep fut `--network none`, `--read-only`, `:ro` adat-mounttal, cap nélkül, és kiszolgál | konténeres smoke teszt |
| S4 | Az authorizer tiltja: `ATTACH`, DML/DDL, `PRAGMA` (az engedetteken kívül), `sqlite_master`, ismeretlen függvény | egységteszt (a kísérlet mintájára) |
| S5 | **FTS-lekérdezésépítő fuzz:** tetszőleges bájtsorozat sem okoz SQL/FTS-szintaktikai hibát, és soha nem értelmeződik operátorként (`NEAR`, `*`, `^`, oszlopszűrő) | `go test -fuzz` |
| S6 | Minden válasz (200/404/405/429/500) hordozza a biztonsági fejléceket | `httptest` |
| S7 | `POST/PUT/DELETE/PATCH/OPTIONS` → 405; nincs süti; nincs kérés-törzs feldolgozás | `httptest` |
| S8 | Lassú fejléc (slowloris) és túlméretes URL/fejléc le van zárva (timeout, 414/431) | integrációs teszt |
| S9 | Rate limit: a hamisított `CF-Connecting-IP` nem megbízható peerről **figyelmen kívül marad**; a limit-tábla mérete korlátos | egységteszt |
| S10 | XSS: minden mezőbe ágyazott payload escape-elve jelenik meg (lista, részletek, `?nyers`, cím, keresődoboz) | golden teszt |
| S11 | Ingest: rosszindulatú upstream fixture-ök (óriás mezők, vezérlő/bidi karakterek, rossz típusok, `null`, óriás tömbök, duplikált és injekciós azonosítók) **nem okoznak pánikot és nem publikálnak félig** | fixture-teszt |
| S12 | **Összeomlás-teszt:** az ingest `kill -9`-cel megölve tetszőleges ponton a publikált fájl érintetlen és érvényes | integrációs teszt |
| S13 | Az áramkör-megszakító leállítja a futást magas érvénytelen-arány / 0 sorozatazonosító esetén | fixture-teszt |
| S14 | Valódi böngészőben a CSP **érvényesítve** fut, a valós oldalakon nincs CSP-sértés | headless Chromium/Firefox |
| S15 | Az image nem tartalmaz OS-csomagot (scratch), `trivy`/`grype` 0 OS-sebezhetőséget jelez | image-vizsgálat |
| S16 | Paritás: a PHP-s és a Go-s változat ugyanazokra a (rögzített) keresésekre ugyanazt a találati halmazt adja (ismert eltérések dokumentálva) | összehasonlító teszt a közös fixture-ökön |
| S17 | Külső szkenner (OWASP ZAP baseline) a futó konténeren nem jelez közepes+ riasztást | kézi/CI |
| S18 | **Kép-fixture-ök:** nem-JPEG tartalom `200` státusszal (pl. a „nem található” HTML), óriás méretek (tömörítési bomba), csonka JPEG, fájl végére fűzött adat, EXIF-fel teli kép → mind elutasítva vagy megtisztítva, a futás nem áll le, nincs pánik | fixture-teszt |
| S19 | **`/img/` bejárás-teszt a SAJÁT szerverünkön:** `..`, kódolt `%2e%2e`, `%00`, szimbolikus link az `images/`-ben, túl hosszú és hibás azonosító → mind 404, soha nem olvas a könyvtáron kívülről | integrációs teszt |
| S20 | A web konténer **nulla kimenő kérést** indít (fut `--network none` mellett, a képekkel együtt) | konténeres smoke teszt |

## 14. Kísérleti eredmények (mérve, 2026-11-05)

Kísérlet: `docs/spikes/sqlite-hardening.go.txt` (Go 1.26.9, rootless podman 5.8, `ncruces/go-sqlite3` v0.36.0). Az adat a valós `daily_program` válasz (165 elem) és egy szintetikus korpusz.

| Állítás | Eredmény |
|---|---|
| FTS5 + `trigram case_sensitive 0 remove_diacritics 1` működik | **igen** (SQLite 3.54.1) |
| Ékezet- és kis-nagybetűfüggetlen részszó-keresés | **igen** (`bék`/`BÉK`/`bek` = 3 találat) |
| 2 karakteres keresés | **0 találat** (trigram minimum 3) |
| Rosszindulatú FTS-szintaxis idézőjelezve | **szó szerinti szöveg**, nincs hiba |
| Statikus, CGO-mentes bináris | **9,06 MB**, `statically linked` |
| Scratch image + `--network none --read-only --cap-drop=ALL`, csak olvasható mount | **fut**; írás hibát ad; nincs `-wal`/`-shm` |
| `ATTACH` csak olvasható kapcsolaton, authorizer nélkül | **lefut (veszély!)** |
| `ATTACH`/DML/DDL/PRAGMA/`sqlite_master` authorizerrel | **mind `denied`**; `SELECT` + FTS működik |
| Atomi csere: régi `immutable` kapcsolat | régi pillanatkép; újranyitva az új adat |
| Fájl tulajdonos `--userns=keep-id` mellett | a hoszt felhasználó |
| Beépülő modulok | **4** |
| Borítókép az upstreamtől | `image/jpeg`, 8–41 KB (átlag ≈ 24,5 KB), 280–720 px széles; `Cache-Control: max-age=86400` |
| Ismeretlen azonosító képe | **`HTTP 200` + 393 B `text/html`** (nem kép) → tartalom-ellenőrzés kötelező |
| Beszúrás + FTS, 100 000 szintetikus sor | 75 s (csak az egyszeri teljes import releváns) |
| Adatbázis-méret, 100 000 szintetikus sor (~1,2 KB szöveg/sor) | 425 MB (≈3,5× a szöveg) |
| Keresés, szintetikus, 98%-os találati arány | 200–400 ms (**legrosszabb eset; valós adaton újramérendő**) |

**Nem mért:** a valós adat méretéhez és szelektivitásához tartozó késleltetés; a `modernc` driver; a CSP böngészői viselkedése; a Quadlet egységek.

## 15. Fázisok

| Fázis | Tartalom | Kapu |
|---|---|---|
| **0 – igazolás** | **nincs régi adat (D6)**: a friss gyűjtésből (kb. 1,2 ezer sor, a PHP-s tesztfuttatáson mérve) származó valós adaton, és a szintetikus korpusszal a növekedésre extrapolálva az FTS-szemantika és a késleltetés összevetése a PHP-val | p95 keresési idő elfogadható; az ékezet/kis-nagybetű viselkedés dokumentálva; döntés a `modernc` vs `ncruces` driverről |
| **1 – mag** | `internal/store` (séma, authorizer, publikálás), `internal/upstream` (kliens + validáció), `m3 ingest daily`, fixture-ök rögzítése az élő API-ból | S4, S11, S12, S13 |
| **2 – web** | `m3 serve`: sablonok (magyar UI), fejlécek, rate limit, lapozás, `?nyers`; saját betű/ikon | S5–S10 |
| **3 – weekly + képek** | sorozatoldal-kivágás, `m3 ingest weekly/item/refresh`, **borítókép-letöltés és -feldolgozás** ([9.1](#91-borítóképek)), `/img/` kiszolgálás | S16, S18, S19 |
| **4 – csomagolás** | Containerfile, Quadlet egységek, cloudflared, mentési rend, README (magyar) | S2, S3, S15 |
| **5 – keményítés** | CSP böngészőben, fuzz, `govulncheck`, ZAP, külső felülvizsgálat | S1, S14, S17 |
| **6 – átállás** | a PHP kód eltávolítása a branchről, `master`-be olvasztás, a régi dokumentáció lezárása | minden kapu zöld |

## 16. Nyitott döntések a felhasználónak

Az **ajánlásom** az első helyen; döntésig ez az alapértelmezés.

| # | Kérdés | Ajánlás |
|---|---|---|
| D1 | **`/add` (kérésre felvétel)?** | **ELDÖNTVE: nem kell** (felhasználó, 2026-11-05). A web így tényleg csak olvasó. Marad a hoszton futtatható `m3 ingest item M3-…` |
| D2 | `?nyers` nyers nézet megmaradjon? | igen (nyilvános adat, paritás) |
| D3 | Borítóképek | **ELDÖNTVE: a Go bináris szolgálja ki helyben tárolt fájlból** (felhasználó, 2026-11-05); a statikus tartalom az exe-be fordítva, a dinamikus borítóképek a `data/images/`-ben ([9.1](#91-borítóképek)). Az M3 így nem látja a látogatókat |
| D4 | CSV/gz export legyen-e (`m3-db.csv.gz`)? | igen, az ingest generálja a data könyvtárba; a web csak rögzített nevű fájlt szolgál ki (nincs könyvtárlistázás); a CSV-injection escape kötelező |
| D5 | A szöveges keresés minimuma 3 karakter? | igen (trigram korlát), UI üzenettel |
| D6 | A régi adat importja | **ELDÖNTVE: nincs régi adat** (felhasználó, 2026-11-05) → nincs `m3 import`, üres adatbázisról indulunk (OPS-4 megerősítve) |
| D7 | HTML gyorsítótár: `max-age=60`? | igen, a Cloudflare cache-szabállyal együtt |
| D8 | Modulútvonal és bináris név | `github.com/py-snake/m3-keresheto-archivum`, bináris: `m3` |
| D9 | PHP kód sorsa a branchen | marad referenciaként a 6. fázis végéig, azután törlés |
| D10 | Licenc | **AGPL-3.0 marad** (a Go-s port az eredeti logikájának származéka; az eredeti szerzői jog fennmarad) |
| D11 | Hol fut? | rootless podman, a hoszt felhasználó alatt; a `/srv/m3/data` helye a hoszt felhasználó döntése |
| D12 | Képek átméretezése? | igen, legfeljebb ~480 px szélesre, JPEG újrakódolással (kisebb tárhely és gyorsabb oldal); egy kis dobozszűrő kell hozzá, mert a standard könyvtárban nincs. A tényleges méret/minőség a 3. fázisban mérendő |
| D13 | A harmadik fél borítóképeinek helyi másolata (szerzői jog/ToS) | a felhasználó felelőssége; **eldöntendő**, mielőtt nyilvánosan elérhetővé tesszük |
| D14 | A böngésző User-Agent karbantartása | **eldöntve:** böngésző UA, konfigurálható; a verziót időnként frissíteni kell (a PHP-ban Chrome/155 a helyi Chromiumból) |

## 17. Tervezett repó-elrendezés (a `go` branchen)

```
go.mod  go.sum
cmd/m3/main.go                       # alparancsok: serve, ingest, backup, export, import, healthcheck
internal/config/                     # környezeti változók (egyetlen hely)
internal/store/                      # SQLite: séma, authorizer, olvasó/író, atomi publikálás
internal/upstream/                   # kliens (allowlist), validáció, parserek (JSON + sorozatoldal HTML)
internal/ingest/                     # daily/weekly/item/refresh
internal/images/                     # letöltés-ellenőrzés, dekódolás/újrakódolás, atomi írás; os.OpenRoot alapú kiszolgálás
internal/web/                        # handlerek, middleware (fejlécek, rate limit), sablonok
web/templates/*.html  web/static/    # beágyazott (embed); saját betű/ikon
tests/fixtures/                      # rögzített valós upstream válaszok + rosszindulatú fixture-ök
deploy/quadlet/*  Containerfile
docs/GO-DESIGN.md  docs/spikes/
(PHP: application/, system/, index.php, … a 6. fázisig referenciaként)
```

## 18. Megvalósítási állapot és eltérések a tervtől

**Kész és tesztelt (2026-11-05):** a teljes program (`cmd/m3`, `internal/*`, `web/`), `Containerfile`, `deploy/quadlet/`, `tests/e2e/`. Tesztek: `go vet`, `go test -race ./...`, 3 fuzzer (hibátlan), `govulncheck` (tiszta), `tests/e2e/run.sh` **51/51** valós M3 oldallal. A biztonsági kritériumok (13. fejezet) állapota a [CLAUDE.md 6. fejezetében](../CLAUDE.md).

**Kimenő proxy-támogatás (a felhasználó kérése, a tervben nem szerepelt):** `UPSTREAM_PROXY` (http:// vagy https:// `CONNECT`-proxy, opcionális `user:password@`), `UPSTREAM_PROXY_INSECURE` (csak a *proxy* TLS-tanúsítványát nem ellenőrzi; a céloldalét soha), `UPSTREAM_CA_FILE`. A céloldal nevét a proxy oldja fel; környezeti `HTTP(S)_PROXY`-t nem használunk; proxy-hibánál nincs visszaesés közvetlen kapcsolatra; a jelszó nem kerül naplóba. Bizonyítás: a gyűjtő egy `--internal` podman hálózaton fut (nincs útja az internetre), és csak a proxyn át működik; a proxyk naplója szerint kizárólag `nemzetiarchivum.hu:443`-ra nyitottak alagutat.

**Eltérések a tervtől:**

| Terv | Megvalósítás | Miért |
|---|---|---|
| `GET /public/…` statikus útvonal | `GET /static/…` | a régi `/public/` a PHP-s elrendezésé volt |
| CSP `img-src 'self'` | `img-src 'self' data:` | az `m3.min.css` apró `data:` SVG-ket tartalmaz (nyilak); a `data:` kép inert |
| oldalplafon ≤100, találatplafon 1000 | oldalplafon **1000**, találatplafon **10 000** | az üres keresés (böngészés) mélyebb lapozást kíván; a költség a mérések szerint elhanyagolható |
| NFC-normalizálás | **nincs** | a `golang.org/x/text` új modul lenne; a vezérlő-, bidi- és cserekarakterek szűrése megvan |
| `m3 import` (régi adat) | **elvetve** | nincs régi adat (D6) |
| `m3 backup` / `m3 export csv` | **nincs** | a mentés a publikált fájl másolása + a `backups/` rotáció; a CSV-export (D4) nincs megvalósítva |
| a sorozat-lapozás kódolása „ellenőrizendő" | **igazolva** | valós oldalon és a fixture-ökön; `page=0 ≡ page=1` |
| rate limit: „pl." értékek | alapérték **3 kérés/s, 15 kitörés** látogatónként; **64** egyidejű kérés | `M3_RATE_LIMIT`, `M3_RATE_BURST`, `M3_MAX_IN_FLIGHT` |
| authorizer függvény-lista (`substr`, `bm25`, `snippet` is) | `match, count, min, max, coalesce, length, lower, upper` | csak ami a lekérdezésekhez kell; plusz `PRAGMA user_version` olvasás |
| a trigram-index mérete csak szintetikusan | **valós**: 10,5 MB / 962 műsor (≈11 KB/műsor) | lásd CLAUDE.md 3. fejezet |
| keresési késleltetés (0. fázis kapu) | **valós adaton p95 = 2,7 ms**, max 4,7 ms | `internal/store/latency_test.go` |
| CSP érvényesítése böngészőben (S14) | **igazolva**: headless Chromium, 0 sértés a valós oldalakon, pozitív kontrollal | |
| Quadlet egységek „kipróbálandók" | a **generátor `--dryrun` hibátlanul elfogadja**; **élesben nem indítottuk** (cloudflared token nélkül) | |

**Séma 2: elérhetőség (2026-11-06).** A leíró oszlopok "első látott", az elérhetőségiek "utolsó megfigyelt" értékek (`playable`, `playable_checked_at`, `playable_changed_at`, `window_first/last`, `gone_at`), plusz append-only `availability_log` (csak változáskor). A megfigyelés a napi/heti kérésekből ingyen jön; a külön `recheck` alapból ki van kapcsolva, és kemény kérés-plafon (`M3_MAX_REQUESTS`) védi az M3 oldalát. Az M3 oldalán mért tények: a műsor-azonosító évekig lekérdezhető, a lejátszhatóság időablakos és DRM-védett; a nem elérhető műsor oldala betölt, de nem játszik le.

**Tanulságok (hibák, amiket a tesztek kaptak el):** az `INSERT OR IGNORE` a `CHECK`-hibákat is elnyelte volna (→ `ON CONFLICT (program_id) DO NOTHING`); az authorizernek a driver és az FTS5 belső `PRAGMA`-it szűken engedni kellett; a Go mux a `..` utakra 307-et ad, nem 404-et.
