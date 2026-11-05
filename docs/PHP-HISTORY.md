# CLAUDE.md – AI munkafüzet (élő dokumentum)

> **Ezt a fájlt a Claude Code minden session elején automatikusan betölti. Ez az egyetlen „igazság forrása" a projekt állapotáról.**
> Minden session köteles: (1) elolvasni, (2) a munka közben frissen tartani, (3) a végén a [Változásnapló](#15-változásnapló)-ba egy sort írni.
> Frissítési szabályok: [0. fejezet](#0-hogyan-tartsd-frissen-ezt-a-fájlt).

- **Utolsó teljes ellenőrzés (élő rendszerek + konténeres végpont-teszt ellen):** 2026-11-05
- **Branch:** `go` (a `master`-ből indult). A `master` a **lezárt, tesztelt PHP-verzió** (3 commit, push nem történt); ez a branch a PHP **teljes Go-s kiváltásának** tervezése és megvalósítása.
- **Aktuális fázis:** **TERV KÉSZ, kód még nincs.** Olvasd el: [docs/GO-DESIGN.md](docs/GO-DESIGN.md) és a lenti [14. fejezetet](#14-a-go-branch). A következő lépés a felhasználó döntése a D1–D11 kérdésekről, majd a 0. fázis.

---

## 0. Hogyan tartsd frissen ezt a fájlt

1. Új hibát találsz → új sor a megfelelő táblába a **következő szabad azonosítóval** (SEC-n / BUG-n / OPS-n / UP-n). Azonosítót **soha ne használj újra**.
2. Hibát javítasz → ne töröld a sort: állítsd `FIXED` státuszra a bizonyítékkal (mit teszteltél, hogyan), és hagyd ott.
3. Élő rendszeren mérsz → a mért tényhez mindig írd oda a **dátumot**. Dátum nélküli „tény" gyanús.
4. Kódra **függvény/fájl névvel** hivatkozz, ne sorszámmal (a sorszámok gyorsan elavulnak).
5. Döntést a **felhasználó** hoz; a [9. fejezetben](#9-telepítési-és-architektúra-tanács-és-nyitott-döntések) csak rögzítsd, ne dönts helyette.
6. A session végén: frissítsd a fejléc dátumait és a [Változásnaplót](#15-változásnapló).

## 1. Munkaszabályok (a felhasználó utasításai)

| Szabály | Forrás |
|---|---|
| **Programkód angolul**: azonosítók, kommentek, commit üzenetek, log üzenetek, a repóban lévő scriptek | felhasználó, 2026-11-05 |
| **UI és README magyarul** | felhasználó, 2026-11-05 |
| Ez a fájl magyarul van (a felhasználó olvassa); az azonosítók és kódrészletek angolok | döntés, 2026-11-05 |
| A SEC/BUG hibák javítása a PHP kódban **engedélyezett** (az előző „ne szerkeszd" szabályt a felhasználó feloldotta) | felhasználó, 2026-11-05 |
| **A `go` branchen az elsődleges szempont a biztonság** (a felhasználó: „akkor felel meg, ha feltörhetetlen”). Ezt őszintén kezeljük: feltörhetetlenség nem garantálható; a [GO-DESIGN 2. fejezete](docs/GO-DESIGN.md#2-mit-jelent-itt-a-feltörhetetlen) írja le, mit ígérünk és mit nem | felhasználó, 2026-11-05 |
| A Go futás: **podman, egyetlen statikus bináris, SQLite fájl a hoszt lemezén**, hogy a hoszt felhasználó menthesse | felhasználó, 2026-11-05 |
| Go-ban **stdlib az elsődleges**; új modul csak indoklással (jelenleg 4 beépülő modul) | terv, 2026-11-05 |
| Commit/push: a felhasználó kérésére. A `master` PHP-lezárását a felhasználó kérte („véglegesítsd”), ezért commitoltam; **push nem történt** | felhasználó, 2026-11-05 |
| A `system/` (CI3 keretrendszer) mappához nem nyúlunk | konvenció |
| Teszteléshez a repó **másolatát** használd (ne szennyezd a munkafát: `curl -o` stb. mindig scratchpadbe) | tanulság, 2026-11-05 |

## 2. Projekt-pillanatkép

- **Mi:** a Nemzeti Archívum **M3** műsorainak kereshető archívuma. Cronok gyűjtik az M3 API-ból és HTML-ből a műsorokat egy MySQL táblába, egyetlen oldalas szerver oldali UI keres bennük.
- **Stack:** CodeIgniter **3.1.13**, MariaDB/MySQL, material-components-web (m3 téma) CSS. Nincs composer/npm/build. Tesztelve: **PHP 8.5.12 + MariaDB 11.8** (podman, `trafex/php-nginx`).
- **Saját kód ≈ 1100 sor**. Licenc: **AGPL-3.0** (`LICENSE.md`), újraírásnál is számít.
- **Git remote:** `origin` = `https://github.com/py-snake/m3-keresheto-archivum` (a felhasználó forkja). Az eredeti: `a-sync/m3-keresheto-archivum` (**archivált**).
- **Helyi eszközök (2026-11-05):** PHP 8.5.11 CLI (**mysqli nélkül**, csak pdo_sqlite/sqlite3), podman 5.8 (rootless), Node 22, Python 3.14, .NET 9, Go. A CI-s végpont-teszthez a podman-os pod kell ([10. fejezet](#10-ellenőrző-receptek)).
- **A projekt jelenleg nem fut sehol**, lásd [OPS-1](#5-üzemeltetési-állapot-ops).

## 3. Térkép

```
index.php                      CI front controller; m3_env() helper; CI_ENV alapértelmezés: production; időzóna Europe/Budapest
application/
  controllers/App.php          GET /, /{n} keresés+lapozás; POST /add (azonosító alapú felvétel: Origin-ellenőrzés, rate limit)
  controllers/Cron.php         cron daily|weekly|backup|csv|refresh  (CLI, vagy HTTP-n X-Cron-Token; refresh csak CLI)
  models/M3.php                parse_program(s)/parse_guides, insert_ignore_programs, get_programs, return_missing_program_ids, update_program
  helpers/curl_helper.php      scrape_url() (hibánál kivételt dob), scrape_json()
  helpers/parser_helper.php    extract_ids(), is_program_id(), parse_program_id_list()
  libraries/MY_Pagination.php  lapozó (reuse_query_string = true)
  views/                       head, search (+JS: POST /add), list, list-raw, foot, cron
  config/                      constants.php (M3_* URL-ek, intervallumok, korlátok), database.php+config.php (env-ből), routes.php (/add)
  cache/                       cron időbélyegek (cron-*.stamp), zárolás (cron-*.lock), rate limit; gitignore-olt
public/                        app.css, m3.min.css (vendor), logo; ide ír a backup/csv (m3-db.gz, m3-db.csv.gz; gitignore-olt)
backup/                        hibás upstream válaszok (*.txt); nginx-nél NEM védett!
tests/run.php                  függőségmentes egységtesztek a helperekre (php tests/run.php)
.sql/m3.skeleton.sql           séma (1 tábla: programs, MyISAM)
deploy/nginx.conf              allowlistes nginx-konfig (SEC-14)
docker-compose.yml             dev: nginx+php (127.0.0.1:80), mariadb (127.0.0.1:3306), adminer (127.0.0.1:8080); CI_ENV=development
system/                        CI3 keretrendszer (ne módosítsd)
```

**Konfiguráció (környezeti változók)** – `m3_env()` a `getenv` / `$_SERVER` / `$_ENV` sorrendben keres:
`CI_ENV` (alap: `production`), `BASE_URL`, `DB_HOST|DB_USER|DB_PASS|DB_NAME` (üres alapérték, kivéve `development`: db/root/rootpass/m3), `CRON_TOKEN` (üres = HTTP cron tiltva), `TRUSTED_PROXIES`, `LOG_THRESHOLD`. A `trafex/php-nginx`-ben a php-fpm `clear_env = no`, ezért az env átjut (mérve).

**Adatfolyam**

```
CLI:  php index.php cron daily  -> /api/m3/v3/daily_program + program_guide?days=30
      php index.php cron weekly -> /m3/get-open -> ?collection=X -> /m3/open?series=S&page=N (HTML) -> /api/m3/v3/item?id=ID (csak hiányzókra)
      php index.php cron backup|csv -> public/m3-db.gz | public/m3-db.csv.gz (tmp+rename)
      mind: parse_program -> INSERT IGNORE INTO programs (program_id UNIQUE)
Böngésző -> App::index -> M3::get_programs (LIKE több oszlopon, ORDER BY id DESC) -> list.php
Kereső JS: ha a szöveg "M3-" kezdetű -> POST /add (same-origin) -> aztán submit
```

Throttle: `daily`/`csv` 8 óra, `weekly`/`backup` 24 óra (`MINIMUM_INTERVAL_*` a `constants.php`-ban); időbélyeg csak **sikeres** futás után frissül. Kilépési kód hibánál 1, HTTP-n 502.

## 4. Hibák és biztonsági rések

Státusz: **FIXED** (a munkafában, tesztelve) / PARTIAL / OPEN / WONTFIX. A „Bizonyíték" azt mondja meg, mit futtattunk (2026-11-05, podman pod: MariaDB 11.8 + php-nginx PHP 8.5.12, élő M3 API ellen).

### 4.1 Biztonság (SEC)

| ID | Súly | Státusz | Leírás → javítás | Bizonyíték |
|---|---|---|---|---|
| SEC-1 | Magas | **FIXED** | SQL injection a `return_missing_program_ids`-ban (nyers `IN ('..')`). → query builder `where_in`, minden azonosító `is_program_id()` (`^M3-[A-Z0-9]{1,27}$`) szűrőn megy át; a `parse_program` is elutasítja a rossz `id`-t | `POST /add` `id=M3-X') OR (SELECT SLEEP(5)) --` → 400, nincs késés; modell-szinten `return_missing_program_ids(["M3-X') OR 1=1 --",...])` → csak az érvényes marad; `tests/run.php` |
| SEC-2 | Magas | **FIXED** | Védtelen állapotváltoztató GET végpontok. → `Cron` konstruktor: CLI vagy `X-Cron-Token` (`hash_equals`, **fail-closed**: `CRON_TOKEN` nélkül 404); `refresh` csak CLI; a régi `/cron/add` megszűnt, helyette `POST /add` (Origin==Host ellenőrzés, ≤10 azonosító, 6 kérés/perc/IP rate limit, JSON válasz) | token nélkül/rosszal 404; `/cron/refresh/5` tokennel is 404; `/cron/add` 404; `GET /add` 405; `POST` Origin nélkül/idegen Originnel 403; 7. kérés → 429 + `Retry-After` |
| SEC-3 | Közepes | **FIXED** | `scrape_url`: TLS-ellenőrzés be, connect 10 s / total 60 s timeout, max 3 redirect, nem 2xx → kivétel | élő API-hívások sikeresek a konténerben; proxy-leállítás szimulációnál kivétel |
| SEC-4 | Közepes | **FIXED** | `CI_ENV` alapértéke `production`, a `.htaccess` már nem erőlteti a developmentet; a compose állítja `development`-re | env nélküli indításnál az ismeretlen útvonal 404, nincs PHP-részlet; dev módban a kódutak 0 warning/deprecation PHP 8.5-ön |
| SEC-5 | Közepes | **PARTIAL** | A titkok már env-ből jönnek (nincs find-replace a config fájlokban). **A `deploy.yaml` elavult**: a keresett szövegek már nincsenek a configban, és FTP-t használ (lásd OPS-6) | – |
| SEC-6 | Közepes | **PARTIAL** | Rate limit (`/add`), keresőszó ≤100 karakter, sorozat-lapozás ≤100 oldal, ≤10 azonosító/kérés. **Nyitva:** a `LIKE '%..%'` keresés indexelhetetlen (FULLTEXT/FTS kellene) | 5000 karakteres keresés → 100-ra vágva |
| SEC-7 | Alacsony | **FIXED** | A `/add` POST + same-origin ellenőrzés. A CI beépített CSRF-je **szándékosan ki van kapcsolva** (cookie-t tenne minden oldalra, és a Cloudflare-es cache-eléssel ütközne) | lásd SEC-2 |
| SEC-8 | Alacsony | **FIXED** | Throttle-fájlok a webrootban → `application/cache/cron-*.stamp` (gitignore-olt); a 4+1 követett `*.cron` fájl törölve a munkafából | – |
| SEC-9 | Alacsony | **FIXED** | Dev hitelesítő adatok a configban → csak `development` módban az alapérték; a compose portjai `127.0.0.1`-re kötve | – |
| SEC-10 | Info | **PARTIAL** | **Felhasználói döntés (2026-11-05): kifelé mindig valódi böngésző User-Agent** (nem saját azonosító). Alapérték: Windows Chrome/155.0.0.0 (a helyi Chromium 155-ből); felülírható `UPSTREAM_USER_AGENT`-tal; a verziót időnként frissíteni kell. **ToS/engedély kérdés továbbra is a felhasználóé** | mérve: az M3 kiszolgál |
| SEC-11 | Alacsony | **FIXED** | `unpkg.com/@mdi/font@latest` (rögzítetlen, SRI nélkül) → `@7.4.47`. A Google Fonts és az unpkg továbbra is külső függőség SRI nélkül | |
| SEC-12 | Alacsony | **ELFOGADVA a PHP-ban, a Go-nál kötelező** | CSV-export: az upstreamtől jövő `=`, `+`, `-`, `@` kezdetű cellák táblázatkezelőben képletként értelmeződhetnek (CSV injection). Döntés: előtag-escape vagy elfogadás | |
| SEC-13 | Közepes | **PARTIAL** | `deploy/nginx.conf`: `nosniff`, `X-Frame-Options`, `Referrer-Policy`, `Permissions-Policy`, **CSP csak Report-Only** (az UI inline scriptet és külső CSS-t/fontot használ; böngészőben nem ellenőriztük az érvényesítést). HSTS a Cloudflare-nél. A szigorú, érvényesített CSP a Go-s változat követelménye | mérve: a fejlécek megjelennek |
| SEC-14 | **Magas** (nginx-nél) | **FIXED** | **A `.htaccess` védelmek nginx alatt nem működnek.** Ha a teljes repó a docroot (mint a dev-compose-ban), kiszolgálódik: `/backup/*` (mentett nyers válaszok), `/application/cache/*`, `/CLAUDE.md`, `/README.md`, `/docker-compose.yml`, `/system/…` és `/application/…` (a PHP fájlok az `exit` guard miatt üresek, de a nem-PHP fájlok olvashatók). A rejtett (`.`) könyvtárakat az image alapkonfigja tiltja (`.sql`, `.git` → 403) | mérve 2026-11-05: `/CLAUDE.md` 200, `/docker-compose.yml` 200, `/backup/` 200, `/.sql/…` 403. **Megoldás:** `deploy/nginx.conf` (allowlist), a compose ezt csatolja | **javítás után mérve:** `/CLAUDE.md`, `/README.md`, `/docker-compose.yml`, `/backup/`, `/application/…`, `/system/…`, `/tests/…`, `/index.php`, `/cron/…`, `/.git/config`, `/.sql/…` mind 404; `/`, `/2`, `/?kereses=`, lapozólinkek, `/public/*`, `POST /add` működik; 6 kB-os POST → 413 |

Rendben lévő (ne „javítsd" feleslegesen): a view-kban minden kimenet `html_escape`-elt (mérve: `<script>` keresés → `[removed]`, nincs reflekció); a keresés query builderrel fut.

### 4.2 Funkcionális hibák (BUG)

| ID | Súly | Státusz | Leírás → javítás | Bizonyíték |
|---|---|---|---|---|
| BUG-1 | Magas | **FIXED** | Ismeretlen `item?id=` → upstream `200` + `null` → `parse_program(null)` TypeError (PHP 8). → `scrape_json` visszaadhat `null`-t („nincs ilyen elem"), a hívók ellenőrzik; `parse_program` `InvalidArgumentException`-t dob; minden `catch` `Throwable`-re | `POST /add id=M3-DOESNOTEXIST` → `{"added":0,"failed":0}`, nincs fatal; selftest: `null/false/'x'/[]` mind elutasítva |
| BUG-2 | Magas | **FIXED** | Upstream hiba csendben „0 új elem", és az időbélyeg mégis frissült. → kivétel hibára/non-2xx-re; időbélyeg csak sikeres futás után; kilépési kód 1 / HTTP 502; hibás válasz mentése `backup/*.txt`-be | `https_proxy=http://127.0.0.1:1` szimulált kiesés: `ERROR: …`, `exit=1`, **nincs** stamp fájl |
| BUG-3 | Közepes | **FIXED** | `show_error($exceptionObject)` és az első blokk hibája kihagyta a másodikat. → forrásonként gyűjtött hibák | ua. |
| BUG-4 | Közepes | **FIXED** | `refresh`: definiálatlan `$info_json`, böngészős `Refresh` láncolás. → CLI ciklus `<from_id>`-tól 1-ig, 0,3 s szünettel, önletiltással (`cron-refresh.disabled`) | `refresh 3` → `updated: 1`, megrongált cím helyreállt |
| BUG-5 | Közepes | **FIXED** | `released` fallback `0000-00-00` (szigorú módban érvénytelen) → `1970-01-01 00:00:00`. **Figyelem:** régi dumpokban még lehet nulla dátum, a nézet mindkettőt kezeli | 0 „ismeretlen" a daily után |
| BUG-6 | Közepes | **PARTIAL (szándékos)** | `INSERT IGNORE` marad: az archívum az **első látott** állapotot őrzi; upsert megváltoztatná a `released`-et (az a görgő lejátszási ablak minimuma). A csendes csonkolás helyett explicit hosszkorlát + log. Frissítésre a `refresh` van. Ha a felhasználó upsertet akar, az külön döntés | |
| BUG-7 | Alacsony | **PARTIAL / javítva a korábbi tévedés** | **Helyesbítés:** az időzónát az `index.php` fixre állítja (`Europe/Budapest`), tehát *nem* szerverfüggő (az első elemzésünk ezt tévesen állította). Marad: a `released` jelentése (az összes dátum minimuma) és a nézet `-1 hour` trükkje | |
| BUG-8 | Alacsony | **FIXED (doku)** | A README a beszúrási sorrendet írja le (ez a valós viselkedés) | |
| BUG-9 | Alacsony | **FIXED** | `duration` = `HH:MM:SS:FF`; a nézet másodpercet is mutat, ha 0 óra és 0 perc | |
| BUG-10 | Alacsony | **FIXED** | `extract_ids` robusztus (nincs falsy-offset, hiányzó záró jelölő nem hiba); sorozatlapozás plafon. UP-8 lezárva | `tests/run.php`; 140 különböző sorozat a weekly futásban hibátlan |
| BUG-11 | Alacsony | **PARTIAL** | `backup`/`csv`: tmp fájl + `rename` (nincs félig írt kiszolgált fájl). Marad: a `dbutil->backup()` az egész dumpot memóriában építi | mentés visszatöltve egy másik adatbázisba: 1165 sor |
| BUG-12 | Alacsony | **FIXED** | Logokban `getMessage()`, mentett válasz csak ha nem üres. A `backup/` takarítása kézi | |
| BUG-14 | **Magas** | **FIXED** | **DB-kapcsolat karakterkészlete `utf8`, a tábla `utf8mb4`**: 4 bájtos karakter (pl. emoji) `????`-vé romlik beszúráskor (reprodukálva MariaDB 11-en a régi beállítással) → `utf8mb4` / `utf8mb4_hungarian_ci` | 🎬 😀 körbeutaztatva helyesen |
| BUG-15 | Közepes | **FIXED** | A `REPLACE` a `refresh`-ben törölte és újraírta a sort (`added` elveszett) → `update_program` (`UPDATE`, `added` és `id` megmarad) | `added` változatlan a refresh után |
| BUG-16 | Alacsony | **FIXED** | `curl_close()` PHP 8.5-ben deprecated → csak PHP < 8-on hívjuk | |
| BUG-17 | Alacsony | **FIXED** | A kereső JS `searchField.readonly` (hibás, `readOnly` kell) | |
| BUG-13 | Info | – | README TODO-k (kártya layout, rádió) nincsenek megvalósítva; kép URL: `https://nemzetiarchivum.hu/images/m3/<program_id>` | |

### 4.3 Adatmodell
- A séma `ENGINE=MyISAM` (nincs tranzakció/FK), az egyetlen index a `PRIMARY` és a `program_id` UNIQUE; **a keresésre nincs index**.
- A séma dump 2020-as, a `NOT NULL` szövegmezőknek nincs alapértéke (szigorú módban az INSERT mindig megadja mindet; a kód ezt teszi).
- Lehetséges, még nem végrehajtott korszerűsítés: InnoDB, FULLTEXT index, `released` nullable. Ez migrációt igényel; **nincs megcsinálva**.

## 5. Üzemeltetési állapot (OPS)

Mérve **2026-11-05**:

| ID | Tény | Következmény |
|---|---|---|
| OPS-1 | `https://m3.devs.space/` és `/index.php` → **302 → `https://github.com/a-sync/m3-keresheto-archivum`**; `/2`, `/app`, `/cron`, a CSS-ek, `/public/m3-db.gz`, `/public/m3-db.csv.gz`, `/robots.txt` → **404**. LiteSpeed + PHP 8.1.34. | **Az éles oldal megszűnt.** A régi DB-dump és CSV már nem tölthető le. |
| OPS-2 | GitHub `a-sync/…`: `archived: true`, `pushed_at: 2026-01-03`; mind az 5 workflow `disabled_manually`; utolsó `cron/daily` futás 2026-01-14: **failure** (ok ismeretlen). Nyitott issue #4 („the end"). | Nincs aktív gyűjtés. |
| OPS-3 | A helyi/fork `master` = `origin/master` = `5684bc2` (2025-02-13) a munka kezdetén. | Fork alapján folytatjuk. |
| OPS-4 | Az upstream csak az aktuálisan elérhető + ~8 napnyi műsort adja; ami lejárt, azt csak a régi dump tartalmazza. Dump nem ellenőrzött (felhasználónál/Wayback). | Dump nélkül a mai ablakból lehet újrakezdeni. A tesztfuttatás 1165 sort gyűjtött (daily 256 + weekly 909). |
| OPS-5 | Új hosting kell. Terv/ötlet: podman → nginx (http) → Cloudflare Tunnel → internet ([9. fejezet](#9-telepítési-és-architektúra-tanács-és-nyitott-döntések)). | |
| OPS-6 | A `.github/workflows/` elavult volt (az `m3.devs.space` címet hívta, FTP-s deploy, nem létező find-replace szövegek). | **Törölve** (2026-11-05, a git előzményben visszaállítható). Cron: CLI + systemd timer. |

## 6. Upstream (M3) API szerződés – **élőben mérve 2026-11-05**

Alap: `https://nemzetiarchivum.hu`. A JSON válaszok `Content-Type: text/html` fejléccel jönnek (ne bízz a content-type-ban).

| Végpont | Használjuk? | Mérés |
|---|---|---|
| `GET /api/m3/v3/daily_program` | igen | 200, 668 KB, `{time, program[]}`, 165 elem |
| `GET /api/m3/v3/program_guide?days=30` | igen | 200, 1,5 MB, `{time, program_guides{"YYYY-MM-DD":[...]}}`, **csak 8 nap / 349 elem** |
| `GET /m3/get-open` | igen | 200, 8 gyűjtemény |
| `GET /m3/get-open?collection=<id>` | igen | `{parent, docs[]}`, összesen 315 doc, 88 `seriesId` |
| `GET /m3/open?series=<seriesId>&page=N` | igen (HTML) | jelölők illeszkednek; **`page=0` ≡ `page=1`**, az utolsó utáni oldal üres (0 bájt); a lapozólink kódolása = PHP `urlencode` (verifikálva) |
| `GET /api/m3/v3/item?id=<id>` | igen | létező: JSON; **ismeretlen: 200 + `null`** |
| `GET /api/m3/v3/programsForSidebar` | nem | 679 KB (új, nem elemzett) |
| `GET /api/m3/v3/live`, `/stream`, `/getTime`, `GET /m3/rss` | nem | élnek |
| `/m3/*` | – | ugyanaz a 39 KB-os SPA-héj; **az M3-nak nincs keresője** → a projekt létjogosultsága megmaradt |

Őszinte UA-val is mind kiszolgál (mérve). **Séma (514 elem):** a parser által olvasott összes mező megvan; nem tárolt mezők: `collection`, `collectionId`, `isBroadcastCatchup_b`, `isHighlighted_b`, `is_live`, `playable`, `rank`, `startTime_dt`, `start_durationTC_str`, `url`, és a teljes `start_*_dts` tömbök. Évek 1961–2016. Maximális hosszak a varchar-korlátok alatt.

## 7. Mezőszótár (`programs` tábla ↔ API ↔ átalakítás)

A `parse_program` (`models/M3.php`) hiányzó/hibás típusú mezőre alapértéket ad; csak az `id` (érvényes formátum) és a `title` kötelező, különben `InvalidArgumentException`.

| DB oszlop (típus) | API mező | Átalakítás | Buktatók |
|---|---|---|---|
| `id` int AI PK | – | auto | az `ORDER BY id DESC` a sorrend |
| `program_id` varchar(31) UNIQUE | `id` | validált `M3-[A-Z0-9]{1,27}` | `RADIO-*` nincs kezelve (a validátor elutasítja) |
| `info`, `extended_info` text | tömbök | `implode("\n")`, csak skalárok | |
| `title` varchar(255) | `title` | trim, ≤255 | kötelező |
| `subtitle` varchar(255) | `subtitle` | trim; **`false` (bool) is jöhet** → `''` | mért: 82/514 `false` |
| `description`, `short_description` text | ua. | trim | a nézet `;` után szóközt szúr be |
| `company` tinytext | `company` | trim, ≤255 **bájt** | |
| `year` smallint | `year` | int | 0 lehet |
| `country` varchar(3) | `country` | trim, ≤3 | |
| `creators`, `contributors`, `genre` text | tömbök | `implode("\n")` | `genre` szabad szöveg |
| `quality`, `pg`, `ratio` varchar(15) | ua. | null → `''` | |
| `duration` varchar(15) | `duration` | nyers, ≤15 | formátum `HH:MM:SS:FF` |
| `hasSubtitle`, `isSeries` tinyint(1) | bool | `!empty` | |
| `seriesId` varchar(255) | `seriesId` | **`base64_decode`** (csak itt) | a weekly a nyers base64-et használja az URL-ekben; ne keverd |
| `episode`, `episodes` smallint | ua. | int | |
| `added` timestamp | – | `DEFAULT CURRENT_TIMESTAMP` | a `refresh` nem módosítja |
| `released` datetime | `start_playable_dts[]` + `start_startTime_dts[]` | **minimum** (UTC → Budapest idő) | fallback `1970-01-01 00:00:00`; érvénytelen dátumstring kihagyva |

CSV oszlopok: `program_id,title,subtitle,episode,episodes,seriesId,quality,year,duration,short_description,released`.

## 8. Upstream-megfigyelések (UP)

| ID | Megfigyelés | Dátum |
|---|---|---|
| UP-1 | Ismeretlen `item?id=` → `200` + `null` | 2026-11-05 |
| UP-2 | `program_guide?days=30` csak 8 napot ad | 2026-11-05 |
| UP-3 | `subtitle` lehet bool `false` | 2026-11-05 |
| UP-4 | `duration` = `HH:MM:SS:FF` | 2026-11-05 |
| UP-5 | JSON `Content-Type: text/html` | 2026-11-05 |
| UP-6 | Az `/m3/*` egy SPA; nincs keresési API | 2026-11-05 |
| UP-7 | Új, nem használt mezők és végpontok (6. fejezet) | 2026-11-05 |
| UP-8 | Sorozatlapozás: a link kódolása megegyezik a PHP `urlencode`-dal; `page=0 ≡ page=1`; a kód 1..max ciklusa helyes. **LEZÁRVA** | 2026-11-05 |
| UP-9 | A gyűjtemény-azonosítók base64-alapúak, a címben zero-width space (`​`) lehet | 2026-11-05 |

## 9. Telepítési és architektúra-tanács és nyitott döntések

*(A felhasználó terve: podman → nginx (http) → Cloudflare Tunnel → internet. A tanács a 2026-11-05-i mérésekre épül; Go-s változat **nincs megírva**, a sebességre/méretre vonatkozó állítások kvalitatívak.)*

### 9.1 A tervezett lánc értékelése
- **Jó irány.** A `cloudflared` csak kifelé kapcsolódik: nincs nyitott bejövő port, az origin IP rejtve marad. A cloudflared→nginx szakasz sima HTTP lehet, **ha privát podman hálózaton / loopbacken marad** (nem hagyja el a gépet). A TLS a Cloudflare szélén ér véget.
- **Feltételek, amik számítanak:**
  1. Az origin **csak a tunnelen át legyen elérhető**: az nginx ne publikáljon host portot (vagy csak `127.0.0.1`), különben a LAN-ból megkerülhető a Cloudflare (WAF, rate limit).
  2. **Valódi kliens IP:** a cloudflared `CF-Connecting-IP`-t ad. nginx: `set_real_ip_from <cloudflared hálózata>; real_ip_header CF-Connecting-IP;`, és az app `TRUSTED_PROXIES` változóját állítsd az nginx hálózatára. Enélkül a `/add` rate limit **egyetlen közös vödör** lesz (minden kérés az nginx IP-járól jön).
  3. `BASE_URL=https://…/`; az `Origin`-ellenőrzés a `Host` fejlécet használja, a cloudflared ezt alapból az eredeti hosttal továbbítja. PHP-nek `HTTPS=on` kell (`X-Forwarded-Proto` alapján).
  4. **SEC-14:** csak `index.php` és `public/` legyen kiszolgálva; a többit az nginx `deny`-val vagy az image tartalmával zárd ki.
  5. A `/cron` HTTP-ról **nem kell**: `location ^~ /cron { return 404; }`, a cronokat `podman exec … php index.php cron daily` + systemd timer (vagy Quadlet) futtassa.
  6. Biztonsági fejlécek (SEC-13) az nginx-ben/Cloudflare-ben; Cloudflare oldalon: rate limiting szabály a `POST /add`-ra, Bot Fight Mode, a `/public/*` cache-elése.
- **nginx minimum-váz (PHP változathoz, ellenőrizetlen minta):**
  ```nginx
  location ~ ^/(application|system|backup|tests|cron|\.) { return 404; }   # SEC-14, cron csak CLI
  location /public/ { try_files $uri =404; }
  location / { try_files /index.php$is_args$args =404; }
  location = /index.php { fastcgi_pass app:9000; include fastcgi_params;
      fastcgi_param SCRIPT_FILENAME $document_root/index.php; }
  # + real_ip (9.1/2. pont), fastcgi HTTPS=on az X-Forwarded-Proto alapján (map), biztonsági fejlécek
  ```
  *(Szándékosan vázlat: élesítés előtt konténerben ki kell próbálni, ahogy a PHP-oldalt is.)*

### 9.2 Konténer-keményítés (podman)
- Rootless, `--read-only` rootfs + `tmpfs /tmp`, írható volume csak: `application/cache`, `application/logs`, `backup`, `public` (exportok); `--cap-drop=ALL`, `--security-opt no-new-privileges`, nem-root felhasználó, `--memory`/`--pids-limit`, image digest-re rögzítve.
- **Szétválasztás (a legnagyobb nyereség):** *web* konténer (csak olvasó DB-felhasználó, **nincs kimenő hálózat**) és *ingest* konténer/timer (író DB-felhasználó, csak `nemzetiarchivum.hu`-ra kimenő). A mostani `/add` a webből ír: ha ezt meg akarjuk szüntetni, egy `pending_ids` tábla + az ingest dolgozza fel (kódváltozás).
- DB: külön konténer, belső hálózat, **nincs publikált port**; élesben nincs adminer; a dev-compose csak fejlesztésre való.
- Titkok: `podman secret` vagy 0600-as env-fájl a repón kívül. Image-frissítés: pinelt + rendszeres újraépítés. A `trafex/php-nginx` közösségi image: élesben saját `Containerfile` (php-fpm alpine + nginx) átgondolandó.

### 9.3 PHP maradjon, vagy újraírás?

| Szempont | PHP (CI3, javítva) | Go + SQLite | Statikus oldal |
|---|---|---|---|
| Biztonság | a fenti javítások után elfogadható; a támadási felület nagyobb (nginx+php-fpm+CI3, írható docroot) | legkisebb szerver-felület: egy statikus bináris, `scratch`/distroless image, típusbiztos (a BUG-1-féle hibaosztály kizárt), `html/template` kontextusfüggő escape, `database/sql` paraméterezett | **nincs szerver**, nincs mit támadni |
| Üzemeltetés | 3 konténer (nginx, php-fpm, DB) + volume-ok | 1 bináris, a DB egyetlen fájl (nincs DB konténer); mentés `VACUUM INTO` | CI épít, Cloudflare Pages szolgál ki |
| Keresés | `LIKE '%x%'` (lassul) | FTS5 (trigram tokenizerrel részszó is) – **az ékezet/kis-nagybetű viselkedést tesztelni kell**, a MySQL `hungarian_ci` szemantikájától eltérhet | kliensoldali (Pagefind/FlexSearch); az index mérete nő |
| Sebesség | elég; a szűk keresztmetszet a DB-keresés, nem a nyelv | a legkisebb memória/indulási idő; a weekly ingest párhuzamosítható | leggyorsabb kiszolgálás (CDN) |
| Fejlesztési idő | 0 (kész) | ~1–2 nap a ~1100 sor miatt (becslés) | ~1–2 nap; **a kérésre-felvétel (`/add`) kiesik** |
| Karbantarthatóság | CI3 régi ág, `system/` kézi frissítés | modern, `go.sum` ellenőrzött függőségek | minimális |

**Ajánlás (a felhasználó dönt):**
1. *Most, gyorsan online:* a javított PHP a fenti keményítéssel és a SEC-14 megoldásával elfogadható.
2. *Hosszabb távra:* **Go + SQLite (FTS5), egyetlen bináris** → ekkor az nginx elhagyható (a cloudflared közvetlenül a Go-s szerverre mutat), vagy meghagyható mélységi védelemnek.
3. *Ha nem kell az `/add`, és a legkisebb üzemeltetési teher a cél:* statikus oldal Cloudflare Pages-en (nincs tunnel, nincs szerver).
4. Más nyelvek: Node/TS (npm ellátási lánc kockázat, nagyobb runtime), Python (jó, de több függőség), .NET 9 (jó, nagyobb image), Rust (túlzás ehhez).

### 9.4 Nyitott kérdések a felhasználónak
- [ ] Melyik út: javított PHP / Go + SQLite / statikus oldal?
- [ ] Megmaradjon-e az `/add` (kérésre felvétel)? Ha igen: marad a web→DB írás, vagy várólistás megoldás?
- [ ] Megvan-e a régi `m3-db.gz` / CSV dump (OPS-4)?
- [ ] Mit kezdjünk a régi workflow-kkal (OPS-6)?
- [ ] Az M3 ToS/engedély (SEC-10)?
- [ ] SEC-12 (CSV-injection) kezelése, SEC-6 (FULLTEXT index → migráció) kell-e?

## 10. Ellenőrző receptek

```bash
# 1) Egységtesztek (nincs külső függőség)
php tests/run.php

# 2) Szintaxis
for f in index.php application/{controllers,models,helpers}/*.php; do php -l "$f"; done

# 3) Élő rendszerek (csak olvasás)
curl -sSI https://m3.devs.space/ ; curl -sS 'https://nemzetiarchivum.hu/api/m3/v3/item?id=M3-DOESNOTEXIST'   # -> null
curl -sS https://api.github.com/repos/a-sync/m3-keresheto-archivum | grep -E '"(archived|pushed_at)"'

# 4) Végponttól végpontig (a helyi PHP-ban nincs mysqli, ezért podman kell)
S=<scratchpad>; mkdir -p $S/app; tar --exclude=.git -cf - . | tar -xf - -C $S/app; chmod -R a+rwX $S/app   # a repó MÁSOLATA
podman pod create --name m3test -p 127.0.0.1:18080:8080
podman run -d --pod m3test --name m3db -e MYSQL_DATABASE=m3 -e MYSQL_ROOT_PASSWORD=rootpass \
  -v $S/app/.sql/m3.skeleton.sql:/docker-entrypoint-initdb.d/m3.skeleton.sql:ro,Z docker.io/library/mariadb:11
podman run -d --pod m3test --name m3web -e DB_HOST=127.0.0.1 -e DB_USER=root -e DB_PASS=rootpass -e DB_NAME=m3 \
  -e BASE_URL=http://localhost:18080/ -e CRON_TOKEN=test-token-123 -v $S/app:/var/www/html:Z docker.io/trafex/php-nginx:latest
# CI_ENV szándékosan nincs megadva -> a production alapértelmezést teszteli
podman exec m3web sh -c 'cd /var/www/html && php index.php cron daily'      # 166 + 90 új sor (2026-11-05)
podman exec m3web sh -c 'cd /var/www/html && php index.php cron weekly'     # ~909 új sor, néhány perc
curl -H 'Origin: http://127.0.0.1:18080' -d 'id=M3-DOESNOTEXIST' http://127.0.0.1:18080/add   # {"added":0,"failed":0}
podman pod rm -f m3test                                                      # takarítás
```
Kiesés szimulálása: `podman exec -e https_proxy=http://127.0.0.1:1 m3web … cron daily` → `exit=1`, nincs stamp. Fejlesztői módú figyelmeztetések: `-e CI_ENV=development`.

**Veszélyes, SOHA ne hívd az éles/harmadik fél rendszerén:** módosító kérést; az M3 felé is csak mérsékelt, olvasó forgalmat (a weekly ~400 kérés).

## 11. Ismert „nem hiba" dolgok
- `{elapsed_time}` a `foot.php`-ban a CI saját helyettesítője.
- A `system/database/drivers/*` többi driver a keretrendszer része; csak `mysqli` használt.
- `RewriteBase /` almappa esetén átírandó (README).
- A CI saját CSRF-védelme szándékosan ki van kapcsolva (SEC-7).

## 12. Jelenlegi teendőlista (prioritás szerint)

1. [ ] A felhasználó döntése a [GO-DESIGN 16. fejezet](docs/GO-DESIGN.md#16-nyitott-döntések-a-felhasználónak) kérdéseiről (D1–D11; a 9.4 PHP-s kérdéseit ez felváltja).
1. [ ] Go 0. fázis: valós adaton FTS-szemantika és késleltetés mérése (kell hozzá a régi dump, vagy a friss gyűjtés: `php index.php cron weekly` a PHP-s podmanban).
2. [ ] Régi DB-dump felkutatása (OPS-4).
3. [ ] Opcionális a PHP-ban: FULLTEXT index + InnoDB migráció (SEC-6). A Go-s változat ezt kiváltja.

## 13. Mit módosítottunk ebben a sessionben (fájlonként)

`index.php` (m3_env, production alap), `.htaccess`, `application/config/{config,constants,database,routes}.php`, `controllers/{App,Cron}.php`, `models/M3.php`, `helpers/{curl,parser}_helper.php`, `views/{cron,head,list,search}.php`, `docker-compose.yml`, `.gitignore`, `README.md`, új: `tests/run.php`, `deploy/nginx.conf`, `CLAUDE.md`. Törölve: `{daily-program,weekly-program,backup,csv}.cron`, `refresh.cron.disabled`, `.github/workflows/*`.

## 14. A `go` branch

**Tervdokumentum:** [docs/GO-DESIGN.md](docs/GO-DESIGN.md) (architektúra, fenyegetésmodell, séma, keresés, HTTP réteg, ingest, SQLite-keményítés, konténer, elfogadási kritériumok S1–S17, fázisok, nyitott döntések D1–D11). **Kísérlet:** [docs/spikes/sqlite-hardening.go.txt](docs/spikes/sqlite-hardening.go.txt).

**A terv lényege (egy bekezdésben):** egy `m3` bináris két szerepben. `m3 serve` (web): `scratch` image, csak olvasható rootfs és adat-mount, **nincs kimenő hálózat**, nincs írási út, SQLite-kapcsolat `immutable=1` + **deny-by-default authorizerrel**. `m3 ingest` (gyűjtő): külön, időzített egyszeri konténer, az upstreamet validálva olvassa, a `m3.sqlite`-ot **másolaton** módosítja, ellenőrzi, majd **atomi `rename`-mel publikálja** (a publikált fájlt soha nem módosítja helyben, ezért a hoszt felhasználó bármikor biztonságosan menti). Keresés: SQLite **FTS5 trigram** (`case_sensitive 0 remove_diacritics 1`).

**Mért tények (2026-11-05, kísérlet) – ezekre építünk, ne vitasd újra mérés nélkül:**
- Driver: `github.com/ncruces/go-sqlite3` **v0.36.0** (SQLite 3.54.1, WASM→Go gépi fordítás, **CGO nélkül**); FTS5-öt **kapcsolatonként kell regisztrálni** (`fts5.Register`), az `embed` csomag elavult; beépülő modulok: **4**.
- FTS5 trigram `remove_diacritics 1`-gyel: ékezet- és kis-nagybetűfüggetlen részszó-keresés; **<3 karakter nem talál**; a felhasználói szöveg mindig idézőjelezett kifejezés.
- **`ATTACH DATABASE` csak olvasható kapcsolaton is lefut** → authorizer kötelező. Az authorizernek engednie kell: `SELECT`; `READ` az app + FTS árnyéktáblákra; függvények: `match`, `count`, …; `PRAGMA query_only` (olvasás/bekapcsolás) és `PRAGMA data_version` (olvasás). A driver minden lekérdezés előtt olvassa a `query_only`-t.
- Statikus bináris 9,06 MB; scratch + `--network none --read-only --cap-drop=ALL` + `:ro` mount fut; nincs `-wal`/`-shm`; `--userns=keep-id` mellett a fájl a hoszt felhasználóé.
- Teljesítmény: **csak szintetikus legrosszabb eset mérve (200–400 ms)**; a valós adaton a p95 a 0. fázis kapuja.

**Nem mért / nyitott:** valós adatra a késleltetés; `modernc.org/sqlite` mint alternatíva; CSP böngészői érvényesítése; Quadlet egységek; a régi adat (OPS-4).

**Fejlesztési környezet a Go-hoz:** Go **1.26.9** (helyi), podman 5.8 rootless, `chromium-browser` és `firefox` megvan (CSP-teszthez). A kísérlet scratchpadje nem tartós; a kísérleti kód a `docs/spikes/`-ban van. Új modul letöltése: `go get` (proxy.golang.org elérhető).

**Munkarend a Go-s fázisokhoz:** minden fázis végén futtasd a [GO-DESIGN 13. fejezet](docs/GO-DESIGN.md#13-tesztelés-és-biztonsági-elfogadási-kritériumok) kapuit, az eredményt írd ide (dátummal), és csak zöld kapu után lépj tovább. A PHP kód referencia marad a 6. fázisig; **ne módosítsd** (a `master`-en van a lezárt állapot).

## 15. Változásnapló

| Dátum | Session | Mi történt |
|---|---|---|
| 2026-11-05 | #1 | Projekt feltérképezése; élő ellenőrzés (m3.devs.space, nemzetiarchivum.hu, GitHub); a fájl létrehozása. |
| 2026-11-05 | #1 (go branch) | `go` branch létrehozva. Kísérlet (ncruces/go-sqlite3 + FTS5 trigram, authorizer, ro mount + atomi csere, scratch konténer), majd [docs/GO-DESIGN.md](docs/GO-DESIGN.md) megírva a mért tényekkel és a D1–D11 nyitott döntésekkel. **Kód még nincs.** |
| 2026-11-05 | #1 (lezárás) | SEC-14 javítva: `deploy/nginx.conf` allowlist (konténerben mérve), SEC-13 részben (fejlécek, CSP Report-Only), OPS-6: workflow-k törölve, README bővítve; a PHP-verzió commitolva a `master`-re. SEC-12 a PHP-ban elfogadva. |
| 2026-11-05 | #1 (folytatás) | SEC-1…4, 7…9, 11 és BUG-1…5, 8…10, 12, 14…17 javítása a PHP-ban; tesztelve podmanban (MariaDB 11.8 + PHP 8.5.12) az élő M3 API ellen (daily + weekly, add, rate limit, kiesés-szimuláció, backup visszatöltés, refresh). Új tételek: SEC-11…14, BUG-14…17. BUG-7 korrigálva (az időzóna fix). README frissítve. Telepítési/architektúra-tanács rögzítve a 9. fejezetben. **Nincs commit.** |
