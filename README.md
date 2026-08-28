# Dozzle PostgreSQL Log Archive Agent

Bu servis, Docker loglarini Vector ile PostgreSQL'e kalici olarak yazar ve ayni veriyi Dozzle `v10.6.14` agent protokolu uzerinden sunar. Dozzle tarafinda kaynak kod degisikligi gerekmez.

Her `svc` bir sanal container olarak gorunur. Container kimligi `sha256(svc)` degerinin ilk 12 karakteridir; `created` ve `started` alani servisin en eski log zamanidir. Arsiv salt okunurdur: action, exec, attach ve update RPC'leri acik bir `FailedPrecondition` hatasi doner.

## Akis

```text
Docker socket -> Vector 0.57 -> POST /ingest -> pgx COPY -> PostgreSQL
                                      |                |
                                      +-> canli fanout +-> gecmis sorgular
                                                           |
Dozzle v10.6.14 <-------------- mTLS gRPC :7007 <----------+
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

Uygulamanin kendi `DATABASE_URL` degeri Compose tarafindan `postgres` servisi ve yukaridaki kimlik bilgileriyle otomatik uretilir. Compose ayrica host sertifika yollari icin `DOZZLE_CERT_FILE` ve `DOZZLE_KEY_FILE` bekler. Baslangic degerleri [.env.example](.env.example) dosyasindadir.
`INGEST_TOKEN` icin en az 32 baytlik, yalnizca URL-guvenli karakterler iceren rastgele bir deger kullanin. Vector 0.57 ortam degiskeni interpolasyonunu varsayilan olarak kapattigi icin compose, kontrollu `INGEST_TOKEN` degerini okuyabilmesi amaciyla ilgili opt-in bayragini etkinlestirir.

`POSTGRES_USER` veya `POSTGRES_PASSWORD`, `postgres-data` ilk kez olusturulduktan sonra degistirilmemelidir. Degiskenleri sonradan degistirmek mevcut veritabanindaki kullaniciyi otomatik guncellemez.

## Sertifika ve Dozzle baglantisi

Dozzle'in belgeledigi Ed25519 sertifika akisini kullanin:

```bash
mkdir -p certs
openssl genpkey -algorithm Ed25519 -out certs/dozzle_key.pem
openssl req -new -key certs/dozzle_key.pem -out certs/request.csr \
  -subj "/C=TR/ST=Istanbul/L=Istanbul/O=Dozzle Archive"
openssl x509 -req -in certs/request.csr -signkey certs/dozzle_key.pem \
  -out certs/dozzle_cert.pem -days 3650
```

Ayni sertifika/key cifti hem arsiv agent'ina hem Dozzle container'ina salt okunur mount edilmelidir. Ornek Dozzle ayarlari:

```yaml
services:
  dozzle:
    image: amir20/dozzle:v10.6.14
    environment:
      DOZZLE_REMOTE_AGENT: "log-archive:7007|Arşiv|Arşiv"
      DOZZLE_CERT: /certs/dozzle_cert.pem
      DOZZLE_KEY: /certs/dozzle_key.pem
    volumes:
      - ./certs/dozzle_cert.pem:/certs/dozzle_cert.pem:ro
      - ./certs/dozzle_key.pem:/certs/dozzle_key.pem:ro
```

Agent istemci sertifikasini zorunlu tutar ve ayni sertifikayi guven koku olarak kullanir. `7007/tcp` Dozzle'dan erisilebilir olmalidir; `8080` yalnizca Compose ic aginda Vector'a acilir.

## Calistirma

```bash
cp .env.example .env
# .env icindeki parola, host, token ve sertifika yollarini duzenleyin
docker compose up -d --build
docker compose logs -f postgres archive vector
```

Coolify'da ayni compose dosyasi tek bir Docker Compose kaynagi olarak deploy edilir. Ayrica managed PostgreSQL olusturmak veya bu uc servis arasinda predefined network acmak gerekmez; hepsi ayni Compose agindadir. Dozzle ayri bir Coolify kaynagiysa, Dozzle ile `log-archive` arasinda erisim icin her iki kaynagi ayni predefined network'e baglayin veya host portu `7007` uzerinden erisin.

Coolify environment alanlarinda en az su degerleri girin:

```text
POSTGRES_USER=logarchive
POSTGRES_PASSWORD=<openssl rand -hex 32 ciktisi>
INGEST_TOKEN=<farkli bir openssl rand -hex 32 ciktisi>
DOZZLE_CERT_FILE=/data/dozzle-log-archive/certs/dozzle_cert.pem
DOZZLE_KEY_FILE=/data/dozzle-log-archive/certs/dozzle_key.pem
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
