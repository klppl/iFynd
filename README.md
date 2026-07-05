# iFynd

En liten Go-tjänst som bevakar [Tradera](https://www.tradera.com) efter
felprisade iPhones och iPads. Den bygger sin egen historik över vad varje
modell faktiskt säljs för, och flaggar köp nu-annonser som ligger rejält
under det.

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

Traderas kategorisidor är Next.js-appar som skickar med hela sökresultatet
som JSON i `self.__next_f.push()`-scriptblock, så ingen HTML-parsning behövs.
JSON:en innehåller dessutom strukturerade attribut (modell, lagring, skick)
som är betydligt mer pålitliga än vad säljaren råkade skriva i titeln.

Varje körning (var 30:e minut som standard):

1. Skrapar sålda annonser till en prishistorik som bara växer. En kategori
   utan historik backfylls med allt Tradera fortfarande visar, ungefär 90
   dagar. Därefter läses bara `IFYND_SOLD_WINDOW_DAYS` dagar bakåt.
2. Skrapar aktiva köp nu-annonser, deduplicerade på Traderas annons-id.
3. Sorterar varje annons i en bucket per (modell, lagring). iPhone-annonser
   har oftast användbara attribut. iPad-annonser har inga alls, så
   titelparsern måste förstå att "3:e gen", "M1" och "2021" kan betyda samma
   enhet. Allt som inte kan klassificeras med säkerhet hoppas över och
   loggas i `skipped_listings`, för en felgissning i prishistoriken är värre
   än en saknad datapunkt. Det gäller tillbehör, paket, reservdelsobjekt,
   flerpack och titlar som bara säger "iPad".
4. Räknar ut median (eller trimmat medelvärde) per bucket och flaggar aktiva
   annonser som ligger mer än `IFYND_THRESHOLD_PCT` procent under. Buckets
   med färre än `IFYND_MIN_SAMPLES` försäljningar ignoreras helt.
5. Notifierar en gång per annons, aldrig mer. Notifieraren är ett interface
   med en loggstubbe bakom `IFYND_NOTIFIER`; ntfy eller Discord kan läggas
   till i `internal/notify`.

## Kör

```sh
go run . --once        # en skrapning+jämförelse, sedan klart
go run .               # loopar var IFYND_INTERVAL (30 min) + HTTP-API
docker compose up -d   # på VPS:en; SQLite ligger i volymen ifynd-data
```

## Webbgränssnitt

`http://<host>:8080/` serverar en dashboard i en enda sida, inbakad i
binären. Filter för familj (iPhone/iPad), modell, fritext och endast fynd.
Varje aktiv rad har två knappar:

- **Trasig** markerar en trasig enhet. Raden blir röd, den kan aldrig bli
  ett fynd, och om enheten senare säljs blockeras priset från historiken.
  Ångra ångrar.
- **Exkludera** tar bort annonsen och kommer ihåg id:t, så att nästa
  skrapning inte smyger tillbaka den.

## HTTP-API

- `GET /healthz`
- `GET /api/status` — statistik från senaste körningen
- `GET /api/listings` — aktiva annonser med referenspriser och flaggor
- `POST /api/listings/{id}/broken` — body `{"broken": true|false}`
- `POST /api/listings/{id}/exclude` — ta bort + blockera en annons
- `GET /api/bargains` — historiska fynd med säljtid
- `GET /api/hits` — träffar från senaste körningen
- `GET /api/buckets` — prishistorikens buckets (antal/min/max/medel)

## Konfiguration (env)

| Variabel | Standard | Betydelse |
|---|---|---|
| `IFYND_DB_PATH` | `ifynd.db` | Sökväg till SQLite (`/data/ifynd.db` i Docker) |
| `IFYND_INTERVAL` | `30m` | Skrapintervall |
| `IFYND_THRESHOLD_PCT` | `15` | Minsta % under referenspriset för att räknas som fynd |
| `IFYND_MIN_SAMPLES` | `5` | Minsta antal sålda innan en bucket litas på |
| `IFYND_MIN_PRICE` | `100` | Hoppa över annonser billigare än så här (skräp/bluff) |
| `IFYND_METRIC` | `median` | `median` eller `trimmed_mean` |
| `IFYND_TRIM_PCT` | `10` | Trimning per svans för `trimmed_mean` |
| `IFYND_LOOKBACK_DAYS` | `90` | Historikfönster för referenspriser |
| `IFYND_SOLD_WINDOW_DAYS` | `14` | Hur långt bakåt inkrementell skrapning läser |
| `IFYND_SOLD_MAX_PAGES` | `20` | Sidtak per inkrementell skrapning av sålda |
| `IFYND_BACKFILL_PAGES` | `100` | Sidtak för första körningens backfyllning |
| `IFYND_ACTIVE_MAX_PAGES` | `25` | Sidtak för aktiva annonser |
| `IFYND_REQUEST_DELAY` | `1500ms` | Paus mellan sidhämtningar (+ jitter) |
| `IFYND_NOTIFIER` | `log` | Notifieringskanal |
| `IFYND_HTTP_ADDR` | `:8080` | Adress för API:t |
| `IFYND_CATEGORIES` | `340186:iphone,342496:ipad` | Tradera-kategorier som `<id>:<familj>`-par |

## Licens

[The Lagom License](LICENSE) — inte för mycket frihet, inte för lite. Precis
lagom.
