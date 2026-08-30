# Dozzle PostgreSQL Log Archive Agent

Bu servis, Docker loglarini Vector ile PostgreSQL'e kalici olarak yazar ve ayni veriyi Dozzle `v10.6.14` agent protokolu uzerinden sunar. Dozzle tarafinda kaynak kod degisikligi gerekmez.

Her `svc` bir sanal container olarak gorunur. Container kimligi `sha256(svc)` degerinin ilk 12 karakteridir; `created` ve `started` alani servisin en eski log zamanidir. Arsiv salt okunurdur: action, exec, attach ve update RPC'leri acik bir `FailedPrecondition` hatasi doner.

## Akis

```text
Docker socket -> Vector 0.57 -> POST /ingest -> pgx COPY -> PostgreSQL
                                      |                |
                                      +-> canli fanout +-> gecmis sorgular
                                                           |
Dozzle v10.6.14 <------ Compose ici mTLS gRPC :7007 <------+
```

- Ingest bicimi: `application/x-ndjson`; her satir `{"svc":"...","ts":"RFC3339","msg":"..."}`.
- Vector, servis adini once `label.coolify.resourceName` etiketinden, yoksa Docker `container_name` alanindan alir.
- Bellek kuyrugu dolarsa `/ingest` `429` ve `Retry-After: 2` doner. Vector disk buffer'i bloklayarak yeniden dener; servis loglari sessizce dusurmez.
- Yazma islemi 1000 satir veya 2 saniyede bir PostgreSQL `COPY` ile yapilir.
- `StreamLogs`, sorgu limitine kadar en yeni kalici kayitlari kronolojik sirada oynatir ve ardindan canli akisa devam eder.
- Tum arsiv kayitlari Dozzle'a `stdout`, `unknown`, `single` olarak iletilir. Ham indirme her mesaja bir satir sonu ekler.

## PostgreSQL

Compose, sabitlenmis `postgres:18.4-alpine3.23` imaji ile `logarchive` veritabanini otomatik olusturur. Veritabani dosyalari `postgres-data` named volume'unda tutuldugu icin container yeniden olusturulsa da kaybolmaz. `archive`, PostgreSQL'e Compose ic aginda `postgres:5432` uzerinden baglanir; dis PostgreSQL URL'si gerekmez.

Uygulama [sql/schema.sql](sql/schema.sql) semasini, icinde bulunulan ayin partition'ini ve bir sonraki ayin partition'ini otomatik olusturur. Varsayilan saklama suresi 6 aydir. Eski veri satir bazli `DELETE` yerine aylik `DROP TABLE` ile kaldirilir.

Sorgu indeksi `logs (svc, ts DESC)` seklindedir. Tek sorguda dondurulen satir sayisi varsayilan olarak 50.000 ile sinirlidir.

## Ortam degiskenleri

| Degisken | Zorunlu | Varsayilan | Aciklama |
|---|---:|---:|---|
| `POSTGRES_USER` | hayir | `logarchive` | Compose PostgreSQL kullanicisi |
| `POSTGRES_PASSWORD` | evet | - | PostgreSQL parolasi; URL-guvenli rastgele deger kullanin |
| `INGEST_TOKEN` | evet | - | `/ingest` Bearer token'i |
| `DOZZLE_CERT` | evet | - | Container icindeki sertifika yolu |
| `DOZZLE_KEY` | evet | - | Container icindeki private key yolu |
| `ARCHIVE_RETENTION_MONTHS` | hayir | `6` | Tutulacak aylik partition sayisi |
| `MAX_ROWS_PER_QUERY` | hayir | `50000` | Tek gRPC gecmis sorgusu limiti |
| `INGEST_MAX_PENDING_ROWS` | hayir | `100000` | Bellekte bekleyebilecek azami satir |
| `GRPC_ADDR` | hayir | `:7007` | Dozzle agent dinleme adresi |
| `HTTP_ADDR` | hayir | `:8080` | Ingest/health dinleme adresi |

Uygulamanin kendi `DATABASE_URL` degeri Compose tarafindan `postgres` servisi ve yukaridaki kimlik bilgileriyle otomatik uretilir. Sertifikalar Coolify volume dogrulayicisi degiskenli source yollarini kabul etmedigi icin sabit olarak `/data/dozzle-log-archive/certs/` dizininden mount edilir. Baslangic degerleri [.env.example](.env.example) dosyasindadir.
`INGEST_TOKEN` icin en az 32 baytlik, yalnizca URL-guvenli karakterler iceren rastgele bir deger kullanin. Vector 0.57 ortam degiskeni interpolasyonunu varsayilan olarak kapattigi icin compose, kontrollu `INGEST_TOKEN` degerini okuyabilmesi amaciyla ilgili opt-in bayragini etkinlestirir.

`POSTGRES_USER` veya `POSTGRES_PASSWORD`, `postgres-data` ilk kez olusturulduktan sonra degistirilmemelidir. Degiskenleri sonradan degistirmek mevcut veritabanindaki kullaniciyi otomatik guncellemez.

## Sertifika ve Dozzle baglantisi

Coolify sunucusunda Compose'un bekledigi sabit dizinde Ed25519 sertifikasini olusturun:

```bash
sudo mkdir -p /data/dozzle-log-archive/certs
cd /data/dozzle-log-archive/certs
sudo openssl genpkey -algorithm Ed25519 -out dozzle_key.pem
sudo openssl req -new -key dozzle_key.pem -out request.csr \
  -subj "/C=TR/ST=Istanbul/L=Istanbul/O=Dozzle Archive"
sudo openssl x509 -req -in request.csr -signkey dozzle_key.pem \
  -out dozzle_cert.pem -days 3650
sudo chmod 644 dozzle_cert.pem
sudo chmod 600 dozzle_key.pem
```

Deploy etmeden once iki yolun da dosya oldugunu dogrulayin: `sudo test -f /data/dozzle-log-archive/certs/dozzle_cert.pem && sudo test -f /data/dozzle-log-archive/certs/dozzle_key.pem && echo OK`. Dosyalar yokken deploy edilirse Docker bu yollarda dizin olusturabilir ve container baslatilamaz.

Ayni sertifika/key cifti hem arsiv agent'ina hem Dozzle container'ina salt okunur mount edilir. Compose icindeki sabitlenmis `amir20/dozzle:v10.6.14` servisi su agent ayarlariyla hazir gelir:

```yaml
DOZZLE_REMOTE_AGENT: "archive:7007|Arşiv|Arşiv"
DOZZLE_CERT: /certs/dozzle_cert.pem
DOZZLE_KEY: /certs/dozzle_key.pem
```

Agent istemci sertifikasini zorunlu tutar ve ayni sertifikayi guven koku olarak kullanir. `7007/tcp` host'a publish edilmez; yalnizca ayni Compose agindaki Dozzle tarafindan erisilir. Archive'in `8080` portu da yalnizca Vector icindir. Disariya acilacak tek web servisi Dozzle'in `8080` portudur.

## Calistirma

```bash
cp .env.example .env
# .env icindeki parola ve token degerlerini duzenleyin
docker compose up -d --build
docker compose logs -f postgres archive vector dozzle
```

Coolify'da ayni compose dosyasi tek bir Docker Compose kaynagi olarak deploy edilir. Ayrica managed PostgreSQL veya ayri bir Dozzle kaynagi olusturmak ve predefined network acmak gerekmez; dort servis de ayni Compose agindadir.

Coolify Compose servisleri yüklendikten sonra yalnizca `dozzle` servisine domain verin:

```text
https://dozzle.sumartiot.com:8080
```

Buradaki `:8080`, proxy'nin container icinde hangi porta yonlenecegini belirtir; kullanici Dozzle'a normal HTTPS/443 uzerinden erisir. `archive`, `postgres` ve `vector` servislerine domain vermeyin. Ayni domaini kullanan eski Dozzle kaynagi varsa once durdurun veya domaini ondan kaldirin. Dozzle'i public etmeden once mevcut kimlik dogrulama ayarlarinizi bu `dozzle` servisine tasiyin ya da Coolify/harici proxy katmaninda erisim korumasi etkinlestirin.

Coolify environment alanlarinda en az su degerleri girin:

```text
POSTGRES_USER=logarchive
POSTGRES_PASSWORD=<openssl rand -hex 32 ciktisi>
INGEST_TOKEN=<farkli bir openssl rand -hex 32 ciktisi>
```

`postgres-data` ve `vector-data` volume'larini Coolify yedekleme politikaniza ekleyin. Asil log arsivi `postgres-data` volume'undadir.

Saglik kontrolu PostgreSQL'e gercek ping atar:

```bash
docker compose exec archive wget -qO- http://127.0.0.1:8080/healthz
```

Manuel ingest ornegi:

`8080` host'a publish edilmez; manuel ingest testi Compose agindaki bir container'dan `http://archive:8080/ingest` adresine yapilmalidir.

## Dogrulama

```bash
go test ./...
go vet ./...
```

Bir milyon satirlik performans kabul testi opt-in'dir ve yalnizca atilabilir bir veritabaninda calistirilmalidir:

```bash
TEST_DATABASE_URL='postgres://postgres:postgres@localhost:5432/logarchive_test?sslmode=disable' \
  go test -run TestMillionRowsQueryLatency -timeout 15m ./internal/storage
```

Test 1.000.000 satiri toplu yazar, en yeni 50.000 satirin sorgusunu olcer ve 500 ms sinirini denetler. Gercek sonuc disk, PostgreSQL ayarlari ve ag gecikmesine baglidir.

PostgreSQL kesintisinde HTTP sagligi `503`, kuyruk doldugunda ingest `429` olur. Vector'in disk buffer'i veriyi saklar ve PostgreSQL geri geldiginde gonderim devam eder. Agent yeniden baslatildiginda servis listesi ve gecmis loglar PostgreSQL'den yeniden kurulur.
