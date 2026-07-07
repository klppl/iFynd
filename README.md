# iFynd

En liten Go-tjänst som bevakar [Tradera](https://www.tradera.com) efter
felprisade iPhones. Den bygger sin egen historik över vad varje modell
faktiskt säljs för, och flaggar köp nu-annonser som ligger rejält under det.

Fliken **Aktiva annonser** är radarn: allt som är till salu just nu, jämfört
med medianen av sålda exemplar med exakt samma modell och lagring. Fynd blir
gröna. Visar sig ett fynd vara en sprucken skärm i förklädnad markerar ett
klick den som trasig (röd), och priset hålls utanför statistiken.

![Aktiva annonser](docs/active-listings.png)

**Sålda fynd** är facit: alla försäljningar de senaste 90 dagarna som gick
under medianen, och hur länge annonsen låg ute innan någon högg. De bästa
brukar gå inom ett dygn, vilket är hela anledningen till att det här går på
timer istället för att jag sitter och uppdaterar sidan.

![Sålda fynd](docs/sold-bargains.png)

## Så funkar det

Var 30:e minut skrapas Traderas sålda och aktiva annonser. Varje annons
sorteras i en bucket per modell och lagring, och aktiva köp nu-annonser
jämförs med medianpriset för sålda i samma bucket. Ligger priset tillräckligt
långt under flaggas det som fynd och notifieras, en gång per annons. Allt som
inte kan klassificeras med säkerhet — tillbehör, paket, reservdelsobjekt,
titlar utan tydlig modell eller lagring — hoppas över, för en felgissning i
prishistoriken är värre än en saknad datapunkt.

## Kör

```sh
go run . --once   # en skrapning+jämförelse, sedan klart
go run .          # loop + webbgränssnitt på :8080
```

## Docker

En färdig image finns på `ghcr.io/klppl/ifynd:latest`, byggd av GitHub
Actions-workflowet i repot (körs manuellt från Actions-fliken).

```yaml
services:
  ifynd:
    image: ghcr.io/klppl/ifynd:latest
    restart: unless-stopped
    ports:
      - "8080:8080"
    volumes:
      - ifynd-data:/data

volumes:
  ifynd-data:
```

```sh
docker compose pull && docker compose up -d
```

## Webbgränssnitt och API

`http://<host>:8080/` visar aktiva annonser och historiska fynd, med filter
för modell och fritext. Filtren speglas i URL:en så att en filtrerad vy går
att bokmärka eller dela — `/?model=iPhone+16`, `/?view=bargains`,
`/?q=pro+max&hits=1` (modellen matchas tolerant, så `/?model=iphone16`
funkar också). Varje annons har två knappar: **Trasig**
(röd, kan aldrig bli fynd, priset hålls utanför statistiken) och
**Exkludera** (tas bort och kommer inte tillbaka). Bakom sidan ligger ett
litet JSON-API — `/api/listings`, `/api/bargains`, `/api/hits`,
`/api/buckets`, `/api/status` — plus `/healthz`.

Är sidan nåbar från internet, sätt `IFYND_PUBLIC=true` och ett
`IFYND_WEB_PASSWORD`. Då är läsning fortfarande öppen, men knapparna
kräver inloggning (knappen **Logga in** uppe till höger).

## Konfiguration

Allt styrs med miljövariabler. De viktigaste:

| Variabel | Standard | Betydelse |
|---|---|---|
| `IFYND_INTERVAL` | `30m` | Hur ofta Tradera skrapas |
| `IFYND_THRESHOLD_PCT` | `15` | Minsta % under medianen för att räknas som fynd |
| `IFYND_MIN_SAMPLES` | `5` | Minsta antal sålda innan en bucket litas på |
| `IFYND_NOTIFIER` | `log` | Notifieringskanal |
| `IFYND_DB_PATH` | `ifynd.db` | SQLite-databasen (`/data/ifynd.db` i Docker) |
| `IFYND_PUBLIC` | `false` | Sätt till `true` när GUI:t är nåbart från internet |
| `IFYND_WEB_PASSWORD` | — | Krävs när `IFYND_PUBLIC` är på; låser upp Trasig/Exkludera |

Resten — sidtak, skrapfönster, kategorier med mera — har vettiga
standardvärden och finns i `loadConfig()` i `main.go`.

## Underhåll

Om en kategori tas bort ur `IFYND_CATEGORIES` (t.ex. iPad/MacBook) ligger
dess gamla annonser kvar i databasen. Rensa dem med en engångskörning som
tar bort allt som inte längre är konfigurerat:

```sh
go run . --prune-retired
# i Docker:
docker compose exec ifynd /ifynd --prune-retired
```

## Licens

[The Lagom License](LICENSE) — inte för mycket frihet, inte för lite. Precis
lagom.
