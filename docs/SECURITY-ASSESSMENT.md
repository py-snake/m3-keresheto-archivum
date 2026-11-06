# Biztonsági kockázatértékelés: mekkora a kockázata az interneten futtatásnak?

*Állapot: 2026-11-06. Őszinte értékelés, nem marketing. Ez nem független audit: a kódot és a teszteket ugyanaz a szerző (az AI) írta, ezért a vakfoltjaik közösek.*

## Röviden

* **Nulla kockázat nincs.** Interneten elérhető szolgáltatás mindig támadható, ez sem kivétel.
* A program **kicsi, szűk felületű, és úgy van építve, hogy egy hiba hatása korlátos legyen**. A PHP-s változathoz képest a kockázat várhatóan jóval kisebb (ezt nem mértem, a felület szűkebb és a futtatási környezet szigorúbb).
* A leggyakoribb valós támadás a **terhelés/zaj** (botok, szkennerek): ez szinte biztosan jönni fog, hatása legfeljebb az oldal átmeneti elérhetetlensége.
* A **gép feltöréséhez egyszerre két, egymást követő hibának kellene fennállnia**: egy kihasználható hibának a programban (vagy a Go/SQLite függőségeiben), és egy konténerkitörési hibának a kernelben/podmanban. Egy véletlenszerű szkenner ezt gyakorlatilag nem éri el; egy célzott, felkészült támadó elvileg igen, mint bármely internetre kitett szolgáltatásnál.
* A **legnagyobb, tőlem eredő kockázat nem a kódban van, hanem abban, hogyan futtatod** (lásd lent: „Amit a telepítés előtt meg kell tenned").

## Mi érhető el kívülről

A Cloudflare Tunnelen és az nginx-en át **egyetlen** dolog: a `m3 serve` webszerep (HTTP, GET/HEAD). Nincs nyitott bejövő port a gépen. A webszerep:

* nem fogad feltöltést, nincs bejelentkezés, süti, munkamenet, adatbázis-írás;
* nincs kimenő hálózata, nincs titka (a Cloudflare token a cloudflared konténerben van);
* csak olvasható fájlrendszerrel és csak olvasható adat-mounttal fut, `scratch` image-ből (nincs shell, nincs értelmező);
* az adatbázist deny-by-default SQLite-authorizeren át éri el (csak `SELECT`).

Felületei: keresés (HTML), borítóképek, statikus fájlok, REST API (JSON), letöltések (fix fájlnevek), egy kis `search.js`.

## Sebezhetőségek: mi ismert és mi nem

* **Ismert sebezhetőség a használt kódban: 0** (`govulncheck`, 2026-11-06, a Go standard könyvtárral és mind a 4 beépülő modullal együtt). Ez csak a **már ismert** hibákra igaz, naponta változhat; a Go és a függőségek frissítése folyamatos teendő.
* **Ismeretlen sebezhetőségek száma: nem tudom megmondani.** Senki sem tudja. Egy ~6000 soros (tesztek nélkül), szűk felületű Go programnál a távoli kódfuttatás valószínűsége alacsony, de nem nulla.
* **Amit a saját tesztjeim találtak a fejlesztés közben** (ez mutatja, hogy a kódban voltak hibák, és hogy a tesztek nem feleslegesek; ami nem akadt fenn, az nem ismert):
  * `INSERT OR IGNORE` csendben elnyelte volna a megszegett megszorításokat (adatintegritás);
  * a JSON `null` „nem elérhető" megfigyeléssé vált volna (adatintegritás);
  * egy átlagos oldalmegtekintés a képekkel együtt túllépte a sebességkorlátot (elérhetőség);
  * a letöltési időkeret számítása irreálisan nagy fájlméretnél túlcsordult;
  * az `ATTACH DATABASE` csak olvasható kapcsolaton is lefutott (tervezési lelet: ezért van az authorizer).
* **Amit a tesztek lefednek:** XSS minden mezőben és az API-ban, SQL/FTS-injekció (fuzz, összesen több százezer futás), útvonal-bejárás és szimbolikus linkes kitörés (kép, letöltés), hamisított `X-Forwarded-For`, lassú fejléc (slowloris), túlméretes URL/fejléc, összeomlás-teszt (a publikált adatbázis nem sérül), a CSP valódi böngészőben, a webszerep hálózat nélkül.
* **Amit NEM csináltam meg:** független pentest vagy kódáttekintés; hosszú (órás) fuzz-futások; elosztott (több gépről induló) DoS-próba; a Quadlet egységek tényleges, élő indítása a Cloudflare Tunnellel; `grype` (a `trivy` megvolt).

## Futtatott szkennerek (2026-11-06, a saját helyi szerverünk ellen)

* **trivy 0.76** az image-en (Go bináris, nincs OS-csomag): **1 HIGH**: `CVE-2026-111943`, Go stdlib `crypto/x509` (köztes tanúsítványokkal MITM), javítva a Go **1.26.10**-ben; a build 1.26.9-et használ. A `govulncheck` ugyanezt nem jelzi, mert a sérülékeny függvény nincs elérhető hívási úton, és a web folyamatnak nincs kimenő TLS-e; a kimenő gyűjtő (M3 és proxy felé) használ `crypto/x509`-et. **Javítva (2026-11-06):** Go 1.26.10 (`go.mod`, `Containerfile`, digestre rögzítve); újraépítés után a trivy 0 találatot ad, `go test -race`, `govulncheck` és az e2e (74/74) zöld. Titok és hibás konfiguráció: nincs.
* **OWASP ZAP baseline (passzív + pók + ajax pók)**: 0 FAIL, 9 figyelmeztetés, 62 szabály rendben. Mind ismert, szándékos vagy információs: `Access-Control-Allow-Origin: *` az `/api/`-n, a `schema.sql` letölthető, cache-elhető oldalak, hiányzó COEP, `Sec-Fetch-Dest`.
* **OWASP ZAP aktív teljes vizsgálat (12 perc)**: 0 FAIL, 5 figyelmeztetés, 138 szabály rendben. Két „magas" riasztás **kézzel ellenőrizve, téves**: az „SQL Injection" a statikus `/static/app.css?v=…` volt (a válasz bájtra azonos minden `v` értékre, a statikus útvonal nem ér adatbázishoz); a „PII Disclosure" egy műsorazonosító számsora (`M3-87A5899…`), nem bankkártya.
* **Terhelés (ApacheBench, saját gép, 1 magnyi kliens)**: ~7000 kérés/s, 200–400 párhuzamos kapcsolat mellett. Az egyidejűségi plafon (64) feletti kérésekre a szerver `503`-at ad (a 200 egyidejű kapcsolatból ~69% kapott 503-at), de **nem omlott össze, a memória 28→40 MB**, és utána azonnal válaszolt. 64 KB-os fejléc: `431` azonnal. Lassú fejlécküldés (1 bájt/s): a szerver 6 mp-en belül bontotta a kapcsolatot. Tétlen kapcsolat: 5 mp-en belül bontva.
* **Mit jelent ez:** egy kliens gépről indított tömeges kérés az oldalt a 64-es plafon miatt *rövid időre válaszképtelenné teheti mások számára*: ez a védelem ára. Valódi DoS ellen a Cloudflare a védelem (sebességkorlát, bot-védelem), nem a Go program.

## Támadási forgatókönyvek (becslések, nem mérések)

| Forgatókönyv | Valószínűség | Hatás | Mit tesz ellene a rendszer |
|---|---|---|---|
| Botok, szkennerek, ismert URL-ek próbálgatása | szinte biztos | nincs (404/405) | fix útvonalak, minden más 404, szigorú fejlécek |
| Túlterhelés / lassú kliensek / sok letöltés | valószínű | az oldal (vagy a letöltés) átmeneti elérhetetlensége, adatvesztés nincs | időkorlátok, egyidejűség-plafon, látogatónkénti sebességkorlát, max. 4 egyidejű letöltés; a Cloudflare enyhít |
| Adat hamisítása az oldalon | nagyon alacsony | az adatbázis hamis tartalma | a web nem ír; a gyűjtő nem érhető el kívülről; az M3-tól jövő adat ellenőrzött, a kimenet escape-elt |
| Rosszindulatú tartalom az M3 oldalról (megmérgezett adat) | alacsony | félrevezető szöveg | validáció, tisztítás, a megszakító nem publikál, szövegként jelenik meg |
| Kódfuttatás a web folyamatban (programhiba vagy függőség hibája) | alacsony, nem nulla | a támadó egy csak olvasható, hálózat nélküli, titokmentes konténerben van | lásd fent |
| Kitörés a konténerből a gépre (kernel/podman/runc hiba) | nagyon alacsony, de a legsúlyosabb | a konténert futtató felhasználó jogai a gépen | rootless podman, `--cap-drop=ALL`, `no-new-privileges`, csak olvasható rootfs; a podman alapértelmezett seccomp-profilja és (SELinuxos gépen) a konténer-címkézés is él, de ezeket **nem én állítottam be és nem teszteltem**: **de ez a felhasználó jogaira korlátozódik, ezért számít, ki ez a felhasználó** |
| A Cloudflare-fiók vagy a tunnel token kompromittálása | alacsony | a névhez tartozó forgalom átirányítása, megszemélyesítés; a gépet közvetlenül nem érinti | a token csak a cloudflared konténerben van; erős jelszó és 2FA a fiókon |
| A cloudflared (külső bináris) hibája | alacsony | a konténerén belüli hozzáférés a belső hálózathoz és az internethez | pin digestre, frissítés |

## A „feltörik a gépem?" kérdés pontos válasza

* **Egy automatikus szkenner:** nagyon valószínűtlen, hogy bejut. Nincs olyan egyszerű bejárat, mint egy feltöltés, jelszóoldal vagy admin felület.
* **Egy célzott támadó 0-day-jel:** elvileg lehetséges, mint bármely internetre kitett szolgáltatásnál, de két egymás utáni hibát kell láncolnia.
* **Mi dönti el a tényleges kockázatot:** nem annyira a program, mint a **telepítés**.

## Amit a telepítés előtt meg kell tenned (ez csökkenti a legtöbbet)

1. **Ne olyan felhasználóval futtasd, amelyik más érzékeny dolgot is kezel.** (Ha a Cloudflare Tunnel → nginx → m3 útvonalat használod, az m3 az nginx felhasználója alatt fut, mert a hálózatuk csak így osztható meg: ekkor a kitörés hatása az a felhasználó minden konténere és secretje. Ez a kompromisszum, lásd a `deploy/INSTALL.md`-t.) A konténerek `--userns=keep-id` módban a futtató felhasználó jogaival fut(hat)nak: egy konténerkitörés ott a **te fájljaidhoz** (otthoni könyvtár, böngészőprofil, SSH-kulcsok) vezetne. Hozz létre egy **dedikált, jogosultság nélküli felhasználót** (`m3`, zárolt jelszóval, `loginctl enable-linger m3`), és az egységeket annak a felhasználónak a könyvtárába telepítsd. Még jobb egy külön virtuális gép vagy külön (akár olcsó) gép/VPS.
2. **Ne tárolj érzékeny adatot azon a gépen/fiókban**, ahol ez fut, és ne legyen elérhető onnan a belső hálózatod (különösen, ha otthoni hálózaton fut).
3. **Tartsd frissen:** a kernelt, a podmant, és építsd újra a binárist a Go és a függőségek frissítésekor (`govulncheck ./...` rendszeresen, pl. hetente).
4. **Pinneld a cloudflared és az nginx image-et digestre**, vagy figyeld a frissítéseket (`AutoUpdate=registry` esetén a változás nem látható előre).
5. **Futtasd le a hiányzó vizsgálatokat** a telepítés előtt: `trivy`/`grype` az image-en, OWASP ZAP baseline az élő szolgáltatáson; idealisan egy független ember nézze át.
6. A Cloudflare oldalon kapcsold be a **sebességkorlátot és a bot-védelmet**, és a fiókon a kétlépcsős azonosítást.
7. Az `M3_TRUSTED_PROXIES` az `m3-internal` hálózat (csak az nginx és az m3-web van rajta). Az nginx konfigurációnak át kell adnia a `CF-Connecting-IP`-t (`deploy/nginx/m3.conf` megteszi). Rosszul beállítva minden látogató egy kliensnek látszik, és a sebességkorlát mindenkit együtt büntet.
8. **Figyeld a naplót** és az időzített feladatok sikerét; az `OnFailure` értesítés még nincs bekötve.

## Ha a legkisebb kockázat a cél

Statikus oldal (CI épít, Cloudflare Pages szolgál ki): nincs szerver, nincs mit támadni. Ennek ára, hogy a gépelés közbeni keresés és az élő API kiesik, vagy kliensoldalra kerül.
