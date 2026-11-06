# Telepítés podmanos szerverre (Cloudflare Tunnel → nginx → m3-web)

Az útvonal: **cloudflared → nginx → m3-web**. A subdomaint az nginx kezeli. A cloudflared **nem** éri el közvetlenül az m3-at: az `m3-web` csak az `m3-internal` hálózaton van, azon pedig csak az nginx és az m3-web.

Ez a leírás a rootless podmanra (4.9 vagy újabb) és a meglévő nginx + cloudflared felállásra készült. Az egységeket a podman 4.9.3 és 5.x Quadlet generátora is hibátlanul elfogadja.

> **Biztonsági megjegyzés.** Az nginx és az m3 hálózata csak ugyanazon felhasználó alatt osztható meg, ezért az m3 ugyanaz a Linux-felhasználó alatt fut, mint az nginx. Ha az m3 konténeréből valaki kitörne, a felhasználó többi dolgát (más konténerek, secretek, `.env` fájlok) is elérné. Az m3 konténerei ezért szigorúak (csak olvasható fájlrendszer, `cap-drop=all`, `no-new-privileges`, a web konténernek nincs kimenő hálózata). Ha ez nem elég, külön felhasználó és külön nginx a tisztább megoldás. Lásd [../docs/SECURITY-ASSESSMENT.md](../docs/SECURITY-ASSESSMENT.md).

## 1. Image

### A) Kis szerveren: az image-et másik gépen építed, és átviszed (ajánlott)

A Go fordító a beágyazott SQLite miatt sok memóriát eszik: **hideg buildnél mérve ~1,1 GB csúcs-memória és ~110 CPU-másodperc** (8 magon 30 mp). Egy 1–2 GB RAM-os VPS ilyenkor swapel és percekig vagy véglegesen elakad. A végeredmény egyetlen 18 MB-os statikus bináris, az image 7 MB tömörítve, ezért érdemes máshol építeni:

```bash
# a fejlesztői gépen, a repó gyökerében (go ág)
podman build --target m3 -t localhost/m3:latest .
podman save localhost/m3:latest | gzip -9 > m3-image.tar.gz
sha256sum m3-image.tar.gz
scp m3-image.tar.gz SZERVER:/tmp/

# a szerveren, a futtató felhasználóként (itt john)
sha256sum /tmp/m3-image.tar.gz                       # egyezzen az előzővel
gunzip -c /tmp/m3-image.tar.gz | podman load         # Loaded image: localhost/m3:latest
podman run --rm localhost/m3:latest version          # m3 go1.26.10
rm /tmp/m3-image.tar.gz
```

A repót a szerveren így is érdemes klónozni (`git clone https://github.com/py-snake/m3-keresheto-archivum.git ~/m3-src && cd ~/m3-src && git checkout go`), de csak az egységek és az nginx-konfig miatt: **buildelni ott nem kell**. Frissítéskor ugyanez: új image építése, átvitele, `systemctl --user restart m3-web.service`.

### B) Elég nagy szerveren: építés helyben

Legalább ~3 GB szabad RAM ajánlott (vagy swap).

```bash
git clone https://github.com/py-snake/m3-keresheto-archivum.git && cd m3-keresheto-archivum
git checkout go
podman build --target m3 -t localhost/m3:latest .
```

## 2. Egységek és adatkönyvtár

Az adat helye tetszőleges, de **a futtató felhasználónak (itt `john`) írnia kell**, mert a gyűjtő konténerek az ő azonosítójával (`--userns=keep-id`) írják az `m3.sqlite`-ot, az `images/`-t és az `export/`-ot. A web konténer csak olvassa (`:ro`). Az egységekben az alapértelmezés `%h/m3/data` (a futtató home-ja); ha az alkalmazásaid a `/srv/apps` alatt vannak, ott a helye:

```bash
sudo mkdir -p /srv/apps/m3/data
sudo chown -R john:john /srv/apps/m3
sudo chmod 755 /srv/apps/m3 /srv/apps/m3/data      # mások (pl. a mentést végző ubuntu) olvashatják; az adat nyilvános

mkdir -p ~/.config/containers/systemd
cp deploy/quadlet/* ~/.config/containers/systemd/
sed -i 's#%h/m3/data#/srv/apps/m3/data#' ~/.config/containers/systemd/m3-*.container   # az adat helye az egységekben
/usr/lib/systemd/user-generators/podman-user-generator --dryrun | head   # a generátor hibát jelez, ha valami nem jó
systemctl --user daemon-reload
```

A `:z` az `Volume=` sorokban SELinux-címkézés; Ubuntún (AppArmor, nincs SELinux) hatástalan, de nem árt.

## 3. Az nginx bekötése az `m3-internal` hálózatra

Az nginx egységébe (`~/.config/containers/systemd/nginx.container`) **egy sort** kell felvenni a `[Container]` szakaszba:

```ini
Network=m3-internal.network
```

(A meglévő `Network=web.network` és `Network=sendit.network` sorok maradnak. A Quadlet drop-in fájlokat a 4.9 nem veszi figyelembe, ezért kell a közvetlen szerkesztés.)

Az nginx konfigurációja: másold a [nginx/m3.conf](nginx/m3.conf) fájlt abba a könyvtárba, amit az nginx `/etc/nginx/conf.d`-ként mountol (nálad: `/srv/web/nginx/conf.d/`), és írd át benne a `server_name`-et a subdomainre.

Az nginx az m3-et **névvel** éri el (`m3-web`, a konténer neve az `m3-internal` hálózaton), de a nevet **kérésenként** oldja fel (`resolver` + változó a `proxy_pass`-ban), nem induláskor. Így az nginx **az m3 leállása esetén is elindul és újratölt**: a látogatók 502-t kapnak, a többi oldalad nem érintett, és az m3 visszajövetele után nem kell az nginx-et újratölteni. Egy sima `proxy_pass http://m3-web:8080` ezt nem tudná: az nginx nem indulna el, amíg a név nem oldható fel.

A `resolver` címe az `m3-internal` hálózat DNS-e, azaz az alhálózat első címe (`10.89.77.1`, lásd `m3-internal.network`). Ellenőrzés: `podman exec nginx getent hosts m3-web` (futó m3 mellett).

## 4. Első adat

```bash
systemctl --user daemon-reload
systemctl --user start m3-ingest-weekly.service      # ~3 perc ~1000 műsorra
journalctl --user -u m3-ingest-weekly -f
systemctl --user start m3-ingest-daily.service
```

A képek letöltése több futásra is eloszolhat (`M3_IMAGES_PER_RUN`).

## 5. Web, nginx, időzítők

```bash
systemctl --user start m3-web.service
podman healthcheck run m3-web                         # 0 = rendben
systemctl --user restart nginx.service                # felveszi az új hálózatot és a konfigot
podman exec nginx nginx -t
podman exec nginx getent hosts m3-web                 # látnia kell az m3-web címét
systemctl --user enable --now m3-ingest-daily.timer m3-ingest-weekly.timer m3-export.timer
systemctl --user start m3-export.service              # egyszer, hogy legyen letölthető fájl
```

Az nginx újraindítása pár másodperc kiesést okoz a többi oldalnak is.

## 6. Cloudflare

A tunnel beállításában add hozzá a subdomaint: szolgáltatás **`http://nginx:8080`** (a meglévő cloudflared az nginx-et a `web` hálózaton éri el). Új tunnel vagy új token nem kell.

A Cloudflare-nél: sebességkorlát és bot-védelem a subdomainre; a fiókon 2FA.

## 7. Ellenőrzés kívülről

Nyisd meg a subdomaint: keresés, egy borítókép, `/api/v1/`, `/export/`. A `POST` kérésre 403-at kell kapnod.

## Üzemeltetés

* **Frissítés:** `git pull`, `podman build --target m3 -t localhost/m3:latest .`, majd `systemctl --user restart m3-web.service`. Az adat nem változik.
* **Mentés:** az `/srv/apps/m3/data/m3.sqlite` (vagy ahová az adatot tetted) bármikor másolható (soha nem módosul helyben, lásd a README-t). A `backups/` a régi változatokat tartja.
* **Első napon** érdemes kikapcsolni az API-t és a letöltéseket (`M3_PUBLIC_API=0`, `M3_PUBLIC_EXPORT=0` a `m3-web.container`-ben), nézni a naplót, és utána bekapcsolni.
* **Ellenőrző parancs a szerveren:** `scripts/ci.sh --full` ugyanezt a kódot futtatja le podmannal (hálózat kell hozzá).
