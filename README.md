# m3-keresheto-archivum

## Telepítés / futtatás
CodeIgniter 3.1 keretrendszert használ az alkalmazás. (PHP 8.1 vagy újabb ajánlott, tesztelve: PHP 8.5, MariaDB 11)  
Ha az oldal valamilyen almappán keresztül érhető el az adott hoszton, a [.htaccess](.htaccess#L10) fájlban a `RewriteBase /` sort szerkeszteni kell és átírni a megfelelő útvonalra.  

Fejlesztő környezet kulcsrakész beindításához elegendő: `docker-compose up`, amivel az alábbi szolgáltatások indulnak (mind csak a `127.0.0.1`-en érhető el):
 * webszerver **localhost:80**
 * adminer: **localhost:8080**
    - server: `db`
    - username: `root`
    - password: `rootpass`
    - database: `m3`
 * adatbázis: **localhost:3306**

## nginx (éles használat)
A [deploy/nginx.conf](deploy/nginx.conf) **allowlistes** konfiguráció: csak a `/`, a `/{szám}`, a `/add` és a `/public/` érhető el, minden más (`application/`, `system/`, `backup/`, `/cron`, `*.md` stb.) 404-et ad. Az Apache-os `.htaccess` védelmek nginx alatt nem működnek, ezért **ezt a konfigot kell használni**; a `docker-compose.yml` is ezt csatolja. Cloudflare Tunnel mögött a fájlban megjegyzésként szereplő `set_real_ip_from` / `real_ip_header` sorokat is be kell kapcsolni (és az alkalmazásnak a `TRUSTED_PROXIES` változót megadni), különben a hívásszám-korlát minden látogatót egy kliensnek lát. A cron feladatok csak parancssorból futnak.

## Beállítások (környezeti változók)
A telepítésfüggő értékek **környezeti változókból** jönnek, a forráskódban nincs titok. A változóknak el kell érniük a PHP-t (php-fpm esetén `clear_env = no` vagy `env[...]` direktíva).

| Változó | Jelentés | Alapérték |
|---|---|---|
| `CI_ENV` | `development` / `testing` / `production` | **`production`** (fejlesztéshez állítsd `development`-re, a `docker-compose.yml` ezt teszi) |
| `BASE_URL` | az oldal címe, záró `/`-lel (pl. `https://pelda.hu/`) | `http://localhost/` |
| `DB_HOST`, `DB_USER`, `DB_PASS`, `DB_NAME` | adatbázis-kapcsolat | csak `development` módban: `db` / `root` / `rootpass` / `m3`, egyébként üres |
| `CRON_TOKEN` | a `/cron/*` HTTP-hívások titka (`X-Cron-Token` fejléc). Ha nincs beállítva, a `/cron/*` HTTP-n **teljesen tiltott** | üres |
| `TRUSTED_PROXIES` | a megbízható fordított proxyk IP-je/CIDR-je (pl. nginx konténer hálózata), a kliens IP-jének (`X-Forwarded-For`) elfogadásához; a hívásszám-korlát ezt használja | üres |
| `LOG_THRESHOLD` | CodeIgniter naplózási szint | production: `1`, egyébként `2` |

A PHP-nak írási jog kell ezekhez: `application/cache/` (cron időbélyegek, zárolás, hívásszám-korlát), `application/logs/`, `backup/`, `public/` (exportok).

## Adatbázis
A séma és tábla szerkezetek exportja az [m3.skeleton.sql](.sql/m3.skeleton.sql) fájlban található. Ez MySQL/MariaDB formátumban van de használható más, CI által támogatott adatbázis is.  

Az éles oldalról elérhető hetente frissülő adatbázis export a [/public/m3-db.gz](https://m3.devs.space/public/m3-db.gz) útvonalon, illetve naponta frissülő részleges CSV export a [/public/m3-db.csv.gz](https://m3.devs.space/public/m3-db.csv.gz) útvonalon.

## Frontend
Egyetlen oldala van, a kereső és lista.  
A kereső minden rögzített adatban keres, nem csupán abban ami megjelenik. A műsorok sorrendje az adatbázisba kerülésük sorrendje (a legutóbb felvett van elöl). A keresőszó legfeljebb 100 karakter lehet.  

Új műsor manuális felvételéhez a kereső mezőben megadható egy vagy több műsor azonosító vesszővel elválasztva (kis- és nagybetűre nem érzékeny).  
*pl.: M3-123456789,M3-987654321*  

A `nyers` paraméter hozzáadása az URL-hez megjeleníti az összes tárolt adatot. (`?nyers` vagy `&nyers` utótag)

### Layout
A material-components-web CSS framework m3 színeivel tematizált változata: [m3-material-components-web](http://github.com/a-sync/m3-material-components-web).

## Backend
Két cronjob letölti, feldolgozza és az adatbázisban rögzíti az aktuálisan elérhető műsorokat.  
Két másik adatbázis biztonsági mentést, és részleges CSV exportot hoz létre.  
A keresőből pedig műsorok adhatók hozzá M3-* azonosító megadásával.  

A cron feladatokat **parancssorból** érdemes futtatni (nincs szükség HTTP-re és nyilvános végpontra):
```
php index.php cron daily
php index.php cron weekly
php index.php cron backup
php index.php cron csv
```
Hiba esetén a kilépési kód `1`, és az időbélyeg nem frissül, így a következő futás újrapróbálja. A feladatok egymás ellen zárolva vannak, és időkorlát is védi őket (napi: 8 óra, heti/mentés: 24 óra).  
HTTP-n (`/cron/daily` stb.) csak a helyes `X-Cron-Token` fejléccel érhetők el, a `refresh` pedig csak parancssorból.

#### [Napi műsor frissítések](https://github.com/a-sync/m3-keresheto-archivum/actions/workflows/cron-daily.yaml)
Útvonal: **/cron/daily**  
A naponta frissülő műsorlistát és a nyolc napos műsorlistát ellenőrzi.  

#### [Heti műsor frissítések](https://github.com/a-sync/m3-keresheto-archivum/actions/workflows/cron-weekly.yaml)
Útvonal: **/cron/weekly**  
A hetente frissülő műsorlista válogatást ellenőrzi.  

#### [Biztonsági mentések](https://github.com/a-sync/m3-keresheto-archivum/actions/workflows/cron-backup.yaml)
Útvonal: **/cron/backup**  
A teljes adatbázist exportálja a **/public/m3-db.gz** tömörített fájlba.  

#### [CSV exportok](https://github.com/a-sync/m3-keresheto-archivum/actions/workflows/cron-csv.yaml)
Útvonal: **/cron/csv**  
Az alábbi mezőket exportálja a **/public/m3-db.csv.gz** tömörített fájlba.  
 * program_id
 * title
 * subtitle
 * episode
 * episodes
 * seriesId
 * quality
 * year
 * duration
 * short_description
 * released

#### Műsor hozzáadása azonosító segítségével
Útvonal: **POST /add** (`id=M3-...,M3-...`)  
Egy vagy több (legfeljebb 10) műsor adatait kéri le és rögzíti az adatbázisban. Több azonosító esetén vesszővel (`,`) elválasztott listát vár. Csak az oldalról (azonos eredetű `Origin` fejléccel) indított POST kérést fogadja el, az azonosítókat szigorúan ellenőrzi, és kliensenként korlátozza a hívások számát (6 / perc). Válasz: JSON, pl. `{"added":1,"failed":0}`.  

#### Adatok frissítése (egyszeri karbantartás)
Parancssorból: `php index.php cron refresh <kezdő_id>`  
A megadott `id`-tól 1-ig újra lekéri a tárolt műsorokat, és helyben frissíti őket (az `added` időpont megmarad). Lefutás után letiltja magát; újrafuttatáshoz töröld az `application/cache/cron-refresh.disabled` fájlt.

## Tesztek
`php tests/run.php`: az azonosító-ellenőrzés és az HTML-kivágás egységtesztjei, külső függőség nélkül.

---

#### TODO:
 * lista helyett kártya layout használata, és minden elérhető adat megjelenítése (kép, besorolás, készítők, felirat stb.)  
 * rádió műsorok rögzítése (RADIO-*)  