# FAZ 0 — Dozzle v10.6.14 agent protokolü inceleme notları

Bu dosya yalnızca kaynak kod incelemesinin sonucudur. Uygulama henüz başlatılmamıştır.

## İncelenen sabit kaynak

- Repo: `https://github.com/amir20/dozzle`
- Tag: `v10.6.14`
- Commit: `c159db5f0b5cccb80e7432d51823eb2702b617bc`
- Commit tarihi: `2026-07-30`
- Yerel kopya: `/tmp/dozzle` (Windows ortamında `D:\tmp\dozzle`)
- Proto dosyaları tamamen okundu:
  - `protos/rpc.proto`
  - `protos/types.proto`
  - `protos/cloud.proto`
- Agent sunucusu: `internal/agent/server.go`
- Hub/agent istemcisi: `internal/agent/client.go`
- Hub bağlantı ve yeniden deneme akışı: `internal/support/docker/retriable_client_manager.go`
- Hub adaptörü: `internal/support/container/agent_service.go`
- TLS sertifika yükleme: `internal/support/cli/certs.go`
- CLI/env tanımları: `internal/support/cli/args.go`
- Agent başlatma ve port: `internal/support/cli/agent_command.go`
- Hub başlangıcı ve bildirim yayınları: `internal/support/cli/clients.go`, `internal/support/docker/multi_host_service.go`
- UI'nin container alanlarını kullanımı: `internal/container/types.go`, `assets/types/Container.d.ts`, `assets/models/Container.ts`, `assets/stores/container.ts`, `internal/web/events.go`, `internal/web/logs.go`

Dozzle'ın bu sürümde kullandığı ilgili Go bağımlılıkları:

- `google.golang.org/grpc v1.82.1`
- `google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af`
- `protoc-gen-go-grpc v1.6.2`

## Agent servisi

- Proto package: `protobuf`
- Servis: `protobuf.AgentService`
- Wire üzerindeki tam metot yolu örneği: `/protobuf.AgentService/HostInfo`
- Go package: `internal/agent/pb`

### RPC imzaları

| RPC | İmza | Tür | Hub'daki kullanım |
|---|---|---|---|
| `ListContainers` | `ListContainers(ListContainersRequest) returns (ListContainersResponse)` | unary | Sidebar/container envanteri; UI event stream açıldığında çağrılır. |
| `FindContainer` | `FindContainer(FindContainerRequest) returns (FindContainerResponse)` | unary | Her tek-container log, tarih aralığı, indirme, action ve terminal akışından önce çağrılır. |
| `StreamLogs` | `StreamLogs(StreamLogsRequest) returns (stream StreamLogsResponse)` | server stream | Açık log görünümünün geçmiş+follow akışı. |
| `LogsBetweenDates` | `LogsBetweenDates(LogsBetweenDatesRequest) returns (stream StreamLogsResponse)` | server stream | Tarihe atlama, geçmişe kaydırma ve filtreli backfill. |
| `StreamRawBytes` | `StreamRawBytes(StreamRawBytesRequest) returns (stream StreamRawBytesResponse)` | server stream | Ham log indirme yolu. |
| `StreamEvents` | `StreamEvents(StreamEventsRequest) returns (stream StreamEventsResponse)` | server stream | UI SSE bağlantısı açılırken subscribe edilir. |
| `StreamStats` | `StreamStats(StreamStatsRequest) returns (stream StreamStatsResponse)` | server stream | UI SSE bağlantısı açılırken subscribe edilir. |
| `StreamContainerStarted` | `StreamContainerStarted(StreamContainerStartedRequest) returns (stream StreamContainerStartedResponse)` | server stream | Log ekranı açıkken sonradan oluşan container'ları akışa ekler. |
| `HostInfo` | `HostInfo(HostInfoRequest) returns (HostInfoResponse)` | unary | İlk gerçek bağlantı/erişilebilirlik çağrısıdır. |
| `ContainerAction` | `ContainerAction(ContainerActionRequest) returns (ContainerActionResponse)` | unary | Actions etkinse start/stop/restart/remove. |
| `UpdateContainer` | `UpdateContainer(UpdateContainerRequest) returns (stream UpdateContainerProgress)` | server stream | Image/container güncelleme işlemi. |
| `ContainerExec` | `ContainerExec(stream ContainerExecRequest) returns (stream ContainerExecResponse)` | bidi stream | Shell/exec. İlk client frame'i hedef ve komutu taşır. |
| `ContainerAttach` | `ContainerAttach(stream ContainerAttachRequest) returns (stream ContainerAttachResponse)` | bidi stream | Attach. İlk client frame'i hedefi taşır. |
| `UpdateNotificationConfig` | `UpdateNotificationConfig(UpdateNotificationConfigRequest) returns (UpdateNotificationConfigResponse)` | unary | Hub başlangıcında bağlı agent'lara otomatik gönderilir. |
| `UpdateCloudConfig` | `UpdateCloudConfig(UpdateCloudConfigRequest) returns (UpdateCloudConfigResponse)` | unary | Hub başlangıcında notification config'den hemen sonra otomatik gönderilir. |
| `GetNotificationStats` | `GetNotificationStats(GetNotificationStatsRequest) returns (GetNotificationStatsResponse)` | unary | Bildirim ekranı istatistik istediğinde çağrılır. |

## Bağlantı sırası ve handshake

1. `DOZZLE_REMOTE_AGENT` değeri `address|name|group` olarak ayrılır. Ad ve grup opsiyoneldir; address zorunludur.
2. `grpc.NewClient` ile lazy bağlantı nesnesi oluşturulur. Client gzip çağrı sıkıştırması, 10 MiB maksimum receive boyutu ve keepalive kullanır.
3. Hub, timeout altında `HostInfo({})` çağırır. TLS bağlantısını fiilen başlatan ve agent'ın kullanılabilir sayılmasını sağlayan ilk RPC budur.
4. Dönen `Host.id`, hub'ın client map anahtarıdır. Aynı ID'yi veren ikinci agent duplicate kabul edilip eklenmez.
5. Agent ilk denemede erişilemezse endpoint failed listesine alınır. Sonraki `ListAllContainers` çağrıları `RetryAndList` üzerinden tekrar `NewClient -> HostInfo` dener.
6. Server mode başlangıcındaki notification manager, bağlı agent'lara sırasıyla `UpdateNotificationConfig`, sonra `UpdateCloudConfig` yollar. Her aşamada agent'lar kendi aralarında paralel çağrılır.
7. Tarayıcı `/api/events/stream` açtığında hub önce `StreamEvents` ve `StreamStats` aboneliklerini başlatır, ardından `ListContainers` çağırıp `containers-changed` SSE mesajını üretir.
8. Tek container log ekranında genel sıra `FindContainer -> StreamLogs` şeklindedir. Tarih sorgusunda `FindContainer -> LogsBetweenDates`; raw indirmede `FindContainer -> StreamRawBytes` kullanılır.
9. Log görünümünde ayrıca `StreamContainerStarted` açılır; bu, görünüm açıkken yeni container geldiğinde yeni `StreamLogs` başlatmak içindir.

Uygulama seviyesinde ayrı bir protocol version request'i, version negotiation veya sürüm uyuşmazlığında bağlantı reddi yoktur. `Host.agentVersion` yalnızca `HostInfo` içinde taşınır. UI, `config.version != host.agentVersion` olduğunda warning badge gösterir; bağlantıyı reddetmez.

TLS/mTLS, uygulama handshake'inden önceki tek zorunlu kimlik doğrulama katmanıdır.

## TLS yükleme ve doğrulama davranışı

Sertifika çifti `tls.LoadX509KeyPair(certPath, keyPath)` ile yüklenir. CLI yolları:

- `DOZZLE_CERT` / `--cert`; varsayılan `dozzle_cert.pem`
- `DOZZLE_KEY` / `--key`; varsayılan `dozzle_key.pem`

Dosyalar yoksa Dozzle binary içine gömülü `shared_cert.pem` ve `shared_key.pem` çiftine düşer. Dosya var ama bozuksa Dozzle fatal ile durur.

Agent server:

- Sunucu sertifikası olarak verilen çifti kullanır.
- Aynı çiftin ilk sertifikasını `ClientCAs` pool'una ekler.
- `tls.RequireAndVerifyClientCert` kullanır; client sertifikası zorunlu ve bu pool'a karşı doğrulanır.
- gRPC server TLS olmadan açılmaz.
- Keepalive enforcement minimum süresi 15 saniyedir; stream yokken keepalive'a izin verir.

Hub client:

- Aynı çifti client certificate olarak sunar.
- İlk sertifikayı `RootCAs` pool'una ekler.
- `InsecureSkipVerify: true` kullanır; kaynak yorumuna göre bunun sebebi endpoint hostname'inin sertifika adıyla eşleşmeyebilmesidir.
- Client keepalive: 30 saniye; timeout 10 saniye; stream yokken keepalive'a izinli.

Sonuç: Tasarlanan agent, Dozzle hub ile aynı cert/key çiftini kullanmalı ve client certificate istemelidir. Sadece tek yönlü server TLS yeterli değildir.

## `rpc.proto` mesajları

Proto3'te `required` anahtar sözcüğü yoktur; aşağıdaki “semantik zorunlu” notları Dozzle'ın kodda alanı doğrudan dereference etmesine veya işlemin alan olmadan anlamlı olmamasına dayanır.

| Mesaj | Alanlar (tag) | Semantik not |
|---|---|---|
| `ListContainersRequest` | `map<string, RepeatedString> filter = 1` | Opsiyonel. Hub UI/user filtrelerini iletebilir. |
| `RepeatedString` | `repeated string values = 1` | Map değer wrapper'ı. |
| `ListContainersResponse` | `repeated Container containers = 1` | Boş liste geçerli. |
| `FindContainerRequest` | `string containerId = 1`; `map<string, RepeatedString> filter = 2` | `containerId` semantik olarak zorunlu; filter opsiyonel. |
| `FindContainerResponse` | `Container container = 1` | Başarılı response'ta `container` zorunlu; hub doğrudan dönüştürür. Bulunamazsa `NotFound` dönmeli. |
| `StreamLogsRequest` | `string containerId = 1`; `Timestamp since = 2`; `int32 streamTypes = 3` | ID zorunlu. `since` server'da nil-safe ele alınır. `streamTypes`: `STDOUT=2`, `STDERR=4`, ikisi `6`. |
| `StreamLogsResponse` | `LogEvent event = 1` | Gönderilen her frame'de event ve event.message olmalı; hub bunları doğrudan açar. |
| `LogsBetweenDatesRequest` | `string containerId = 1`; `Timestamp since = 2`; `Timestamp until = 3`; `int32 streamTypes = 4` | ID, since ve until semantik olarak zorunlu. Referans server `since.AsTime()` ve `until.AsTime()` çağırır. |
| `StreamRawBytesRequest` | `string containerId = 1`; `Timestamp since = 2`; `Timestamp until = 3`; `int32 streamTypes = 4` | ID ve iki tarih semantik olarak zorunlu. |
| `StreamRawBytesResponse` | `bytes data = 1` | Her frame ham byte chunk'ı. Referans chunk boyutu 1024 byte. |
| `StreamEventsRequest` | alan yok | Boş request. |
| `StreamEventsResponse` | `ContainerEvent event = 1` | Frame gönderilecekse event zorunlu. |
| `StreamStatsRequest` | alan yok | Boş request. |
| `StreamStatsResponse` | `ContainerStat stat = 1` | Frame gönderilecekse stat zorunlu. |
| `HostInfoRequest` | alan yok | Boş request. |
| `HostInfoResponse` | `Host host = 1` | Başarılı response'ta host zorunlu; client `info.Host.*` alanlarını doğrudan okur. |
| `StreamContainerStartedRequest` | alan yok | Boş request. |
| `StreamContainerStartedResponse` | `Container container = 1` | Frame gönderilecekse container zorunlu. |
| `ContainerActionRequest` | `string containerId = 1`; `ContainerAction action = 2` | İkisi semantik olarak zorunlu. Enum default'u `Start=0` olduğundan eksik action wire üzerinde Start görünür. |
| `ContainerActionResponse` | alan yok | Başarı yalnızca boş response ile ifade edilir; hata gRPC status'tur. |
| `UpdateContainerRequest` | `string containerId = 1` | ID zorunlu. |
| `UpdateContainerProgress` | `string status = 1`; `string layer = 2`; `int64 current = 3`; `int64 total = 4`; `string error = 5` | `status="done"` görülürse hub `updated=true` yapar; EOF normal tamamlanmadır. |
| `ContainerExecRequest` | `string containerId = 1`; `repeated string command = 2`; oneof `payload`: `bytes stdin = 3` veya `ResizePayload resize = 4` | İlk frame containerId+command; sonraki frame'ler payload taşır. |
| `ResizePayload` | `uint32 width = 1`; `uint32 height = 2` | Resize frame'inde ikisi kullanılır. |
| `ContainerExecResponse` | `bytes stdout = 1` | stdout chunk'ı. |
| `ContainerAttachRequest` | `string containerId = 1`; oneof `payload`: `bytes stdin = 2` veya `ResizePayload resize = 3` | İlk frame containerId; sonraki frame'ler payload. |
| `ContainerAttachResponse` | `bytes stdout = 1` | stdout chunk'ı. |
| `UpdateNotificationConfigRequest` | `repeated NotificationSubscription subscriptions = 1`; `repeated NotificationDispatcher dispatchers = 2` | İki liste de boş olabilir. Referans server 1000 subscription ve 100 dispatcher üst sınırı uygular. |
| `UpdateNotificationConfigResponse` | alan yok | Boş başarı cevabı. |
| `UpdateCloudConfigRequest` | `NotificationCloudConfig cloudConfig = 1` | Nil/boş config cloud dispatcher'ı temizleme anlamındadır. |
| `UpdateCloudConfigResponse` | alan yok | Boş başarı cevabı. |
| `GetNotificationStatsRequest` | alan yok | Boş request. |
| `GetNotificationStatsResponse` | `repeated NotificationSubscriptionStats stats = 1` | Boş liste geçerli. |

## `types.proto` mesajları

| Mesaj/enum | Alanlar (tag) |
|---|---|
| `Container` | `id=1`, `name=2`, `image=3`, deprecated `status=4`, `state=5`, deprecated `ImageId=6`, `created=7`, `started=8`, `health=9`, `host=10`, `tty=11`, `labels=12`, `stats=13`, `group=14`, `command=15`, `finished=16`, `memoryLimit=17`, `cpuLimit=18`, `fullyLoaded=19`, `env=20`, `ports=21`, tag 22 reserved, `restartPolicy=23`, `networkMode=24`, `mountStats=25`, `mounts=26`. |
| `ContainerStat` | `id=1`, `cpuPercent=2`, `memoryUsage=3`, `memoryPercent=4`, `networkRxTotal=5`, `networkTxTotal=6`, `diskReadTotal=7`, `diskWriteTotal=8`. |
| `Mount` | `type=1`, `source=2`, `destination=3`, `rw=4`. |
| `MountStat` | `destination=1`, `total=2`, `free=3`, `used=4`, `available=5`, `lastChecked=6`. |
| `LogFragment` | `message=1`. |
| `LogEvent` | `id=1`, `containerId=2`, `google.protobuf.Any message=3`, `timestamp=4`, `level=5`, `stream=6`, `type=7`, `rawMessage=8`. |
| `SingleMessage` | `message=1`. |
| `GroupMessage` | `repeated LogFragment fragments=1`. |
| `ComplexMessage` | `bytes data=1` (JSON bytes). |
| `ContainerEvent` | `actorId=1`, `name=2`, `host=3`, `timestamp=4`, `actorAttributes=5`, `container=6`. |
| `Host` | `id=1`, `name=2`, `nodeAddress=3`, `swarm=4`, `labels=5`, `operatingSystem=6`, `osVersion=7`, `osType=8`, `cpuCores=9`, `memory=10`, `agentVersion=11`, `dockerVersion=12`, `runtime=13`. |
| `ContainerAction` | `Start=0`, `Stop=1`, `Restart=2`, `Remove=3`. |
| `NotificationSubscription` | `id=1`, `name=2`, `enabled=3`, `dispatcherId=4`, `logExpression=5`, `containerExpression=6`, `metricExpression=7`, `cooldown=8`, `sampleWindow=9`, `eventExpression=10`. |
| `NotificationDispatcher` | `id=1`, `name=2`, `type=3`, `url=4`, `template=5`, `headers=6`; tag 7, 8, 9 reserved. |
| `NotificationCloudConfig` | `apiKey=1`, `prefix=2`, `expiresAt=3`. |
| `NotificationSubscriptionStats` | `subscriptionId=1`, `triggerCount=2`, `lastTriggeredAt=3`, `triggeredContainerIds=4`. |

### Agent için semantik olarak zorunlu `Container` alanları

- `id`: UI ve backend map/route anahtarıdır. İstenen `sha256(svc)` ilk 12 hex kuralı doğru ve stabil olmalıdır.
- `name`: Sidebar ve başlıkta gösterilir; `svc` olmalıdır.
- `state`: Varsayılan UI yalnız `running` container'ları gösterir. `running` zorunludur.
- `host`: Container'ları host altında gruplamak ve log route'una doğru host ID'yi koymak için `HostInfo.host.id` ile aynı olmalıdır.
- `created`: Backend tarih backfill'inde `if to.Before(container.Created) { skip }` kontrolü vardır. Yanlış/yeni bir değer eski arşivi görünmez yapar.
- `started`: Normal log görünümü `StreamLogs` için `since=max(filterStart, container.StartedAt)` kullanır. Yanlış/yeni değer geçmişin ilk açılışta istenmesini engeller.
- `image`, `command`: UI modeli ikisini alır; `storageKey` üretiminde kullanır. Boş olabilir fakat UI bilgisini azaltır.
- `labels`: Nil map hub'da boş map'e çevrilir. UI namespace için sırasıyla `dev.dozzle.group`, `coolify.projectName`, Docker stack/compose label'larına bakar. Yalnız `svc` saklandığı için gerçek `coolify.projectName` geri üretilemez.
- `stats`: Boş olabilir; hub boş history'yi sıfırlarla doldurur.
- `health`, `group`, limits, mounts ve mount stats: temel sidebar/log için opsiyoneldir.
- `status` ve `ImageId`: deprecated; kullanılmamalı.
- `env`, `ports`, `restartPolicy`, `networkMode`, `fullyLoaded`, `tty`: temel log görüntüsü için gerekli değildir.

## LogEvent wire ayrıntıları — kritik

### Timestamp normal protobuf zamanı gibi kullanılmıyor

Dozzle'ın internal `container.LogEvent.Timestamp` değeri Unix milisaniyedir. Referans agent bunu şu şekilde encode eder:

```go
timestamppb.New(time.Unix(event.Timestamp, 0))
```

Hub ise şöyle decode eder:

```go
resp.Event.Timestamp.AsTime().Unix()
```

Dolayısıyla wire üzerindeki `google.protobuf.Timestamp.seconds` alanında gerçek Unix saniyesi değil, Unix milisaniye sayısı taşınır. Yeni agent `timestamppb.New(actualTime)` kullanırsa UI tarihi yaklaşık 1000 kat küçük olur ve 1970 civarında görünür. Uyumlu üretim, RFC3339 `ts` değerini Unix milisaniyeye çevirip bu sayıyı `Timestamp.seconds` olarak koymalıdır. Bu protokol milisaniye hassasiyetine düşer.

Bu anomali yalnız `LogEvent.timestamp` içindir. `Container.created/started/finished`, `ContainerEvent.timestamp`, `MountStat.lastChecked` ve notification timestamp'leri normal protobuf timestamp'tir.

### Message Any ve parse davranışı

Hub `LogEvent.message` alanındaki `Any`'yi açar ve yalnız şu üç tipe göre davranır:

- `protobuf.SingleMessage`
- `protobuf.GroupMessage`
- `protobuf.ComplexMessage`

Unknown veya nil message frame'i güvenli değildir; log kaybolur ya da nil dereference riski oluşur.

Önemli bulgu: Hub, agent'dan aldığı ham satırı yeniden JSON/logfmt/level olarak parse etmez. Parsing normal Dozzle mimarisinde agent tarafındaki `EventGenerator` içinde, gRPC'den önce yapılır. Bu projenin “parse etme, ham sakla” kuralına uygun wire davranışı:

- `message = Any(SingleMessage{message: msg})`
- `type = "single"`
- `rawMessage = msg`
- `level = "unknown"`
- `containerId = stabil servis ID'si`

Bu seçim ham metni doğru gösterir; ancak Dozzle'ın structured JSON görünümü ve otomatik level renklendirmesi çalışmaz. Bu, ham saklama şartının doğrudan sonucudur.

Referans Dozzle `LogEvent.id` değerini ham input satırı üzerinde FNV-1a 32-bit ile üretir. Permalink/`startId`/`lastSeenId` davranışı için aynı algoritma kullanılmalıdır. Aynı metinli iki satırın aynı ID'yi alması referans davranıştır.

`streamTypes` maskesi kaynakta proto enum değildir:

- `UNKNOWN = 1`
- `STDOUT = 2`
- `STDERR = 4`
- `STDALL = 6`

## Container listesinde Dozzle'ın fiilen kullandığı alanlar

| Alan | Kullanım | Arşiv agent sonucu |
|---|---|---|
| `id` | UI map anahtarı, route, açık tab | Mutlaka stabil: `hex(sha256(svc))[:12]`. |
| `name` | Sidebar, başlık, arama | `svc`. |
| `state` | `showAllContainers=false` iken yalnız `running` görünür | Daima `running`. |
| `host` | Host altında gruplama ve `/api/hosts/{host}/...` route'u | `HostInfo.host.id` ile birebir aynı stabil değer. |
| `created` | Tarih backfill'inin alt sınırı | En eski arşiv satırından büyük olamaz. |
| `started` | İlk `StreamLogs.since` değeri | Geçmiş davranışını doğrudan etkiler. |
| `finished` | Stream EOF sonrası stopped zamanı | Running sanal container için zero/boş semantik olarak yeterli. |
| `image` | Tablo/detail ve storage key | Boş veya sabit açıklayıcı değer olabilir. |
| `command` | Detail ve storage key | Boş olabilir. |
| `labels` | Namespace/group/swarm tanıma | En az `coolify.resourceName=svc` üretilebilir; diğer Coolify label'ları ingest şemasında yok. |
| `group` | Container bazlı özel grup | Opsiyonel; hub host grubu zaten endpoint'teki üçüncü parçadan gelir. |
| `stats`, limits | Grafikler | Boş/sıfır kabul edilir. |
| `health`, mounts | Görsel detay/warning | Opsiyonel. |

`DOZZLE_REMOTE_AGENT="log-archive:7007|Arşiv|Arşiv"` değerinde ikinci `Arşiv` host name override, üçüncü `Arşiv` host group'tur. Bunlar agent proto içindeki `Container.group` alanı değildir.

## Boş/no-op dönebilecek RPC'ler

| RPC | FAZ 1 için güvenli minimum davranış | Etki |
|---|---|---|
| `HostInfo` | Boş olamaz; sabit ve non-nil `Host` dönmeli. | Bağlantı ve sidebar host'u için zorunlu. |
| `ListContainers` | Boş liste protokol olarak geçerli, fakat ürün işlevi için DB servislerini dönmeli. | Temel işlev. |
| `FindContainer` | No-op olamaz; bilinen ID için tam sanal Container, bilinmeyen için `NotFound`. | Tüm log yolları önce bunu çağırır. |
| `StreamLogs` | No-op olamaz; en az canlı satırları taşımalı. | Temel işlev. İlk geçmiş replay konusu aşağıda açık karar. |
| `LogsBetweenDates` | Sıfır frame ile temiz EOF geçerli; eşleşen satırlarda event stream zorunlu. | Temel geçmiş işlevi. |
| `StreamRawBytes` | Sıfır frame + temiz EOF hub'ı bozmaz. | Raw download boş olur. İstenirse `msg + "\n"` chunk'larıyla işlevsel yapılabilir. |
| `StreamEvents` | Sıfır frame'li stream temiz kapanabilir veya context bitene kadar açık tutulabilir. | Container lifecycle event'leri gelmez; initial `ListContainers` yine çalışır. |
| `StreamStats` | Sıfır frame'li stream temiz kapanabilir veya açık tutulabilir. | UI sıfır stats gösterir. |
| `StreamContainerStarted` | Sıfır frame teknik olarak kabul edilir. | Açık log ekranı sonradan ilk kez görülen `svc` için otomatik stream başlatmaz; sayfa yenilemek gerekir. Dinamik servisler için event üretmek daha doğru. |
| `ContainerAction` | Sahte başarı dönmemeli; `Unimplemented`/`FailedPrecondition` gRPC status dönmeli. | Actions varsayılan kapalıyken çağrılmaz. |
| `UpdateContainer` | Sıfır progress + temiz EOF teknik olarak `updated=false, nil` üretir; açık hata daha dürüst. | Update özelliği arşivde anlamsız. |
| `ContainerExec` | Açık `Unimplemented`/`FailedPrecondition`. | Shell varsayılan kapalı; sahte terminal açılmamalı. |
| `ContainerAttach` | Açık `Unimplemented`/`FailedPrecondition`. | Shell varsayılan kapalı; sahte başarı verilmemeli. |
| `UpdateNotificationConfig` | Boş response ile config'i kabul edip no-op olabilir. | Hub başlangıcında otomatik çağrıldığı için RPC mutlaka register edilmelidir. |
| `UpdateCloudConfig` | Boş response ile no-op olabilir. | Hub başlangıcında otomatik çağrıldığı için RPC mutlaka register edilmelidir. |
| `GetNotificationStats` | `stats=[]` dönebilir. | Bildirim istatistikleri sıfır görünür. |

Not: `UnimplementedAgentServiceServer` embed etmek yeni RPC'lere forward compatibility sağlar, fakat bu fazın gereksinimi mevcut 16 RPC'nin tamamını açıkça implemente etmektir. Anlamsız mutation/terminal RPC'leri “başarılıymış” gibi davranmak yerine kontrollü gRPC hatası döndürmelidir.

## `cloud.proto` — ayrı ve kapsam dışı servis

`cloud.proto`, `DOZZLE_REMOTE_AGENT` bağlantısının parçası değildir. Ayrı package/service tanımlar:

- Package: `cloud`
- Service: `cloud.CloudToolService`
- `ToolStream(stream ToolResponse) returns (stream ToolRequest)`
- `SearchLogs(SearchLogsRequest) returns (SearchLogsResponse)`

Bu servis Dozzle ile Dozzle Cloud arasındadır; Postgres archive agent'ın register etmesi gereken servis değildir. Dosya yine de tamamen okunmuş ve yüzeyi aşağıda kayda geçirilmiştir.

| Mesaj/enum | Alanlar (tag) |
|---|---|
| `ToolRequest` | `request_id=1`; oneof type: `list_tools=2`, `call_tool=3`, `cancel_stream=4`. |
| `ToolResponse` | `request_id=1`; oneof type: `list_tools=2`, `call_tool=3`, `log_batch=4`. |
| `LogBatch` | `entries=1`. |
| `LogBatchEntry` | `host_id=1`, `container_id=2`, `container_name=3`, `timestamp_ns=4`, `message=5`, `stream=6`, `level=7`, `log_id=8`. |
| `ListToolsRequest` | alan yok. |
| `ListToolsResponse` | `tools=1`, `version=2`. |
| `ToolDefinition` | `name=1`, `description=2`, `parameters_json=3`, `scope=4`, `read_only=5`. |
| `ToolScope` | `UNSPECIFIED=0`, `INSTANCE=1`, `HOST=2`, `CONTAINER=3`. |
| `CallToolRequest` | `name=1`, `arguments_json=2`. |
| `CallToolResponse` | `success=1`, `error=2`, `stream=9`, `end_stream=10`; oneof result: `list_hosts=3`, `list_containers=4`, `container_stats=5`, `action=6`, `fetch_logs=7`, `inspect_container=8`, `deploy=11`, `notification=12`. |
| `CancelStreamRequest` | `stream_request_id=1`. |
| `HostInfo` | `id=1`, `name=2`, `n_cpu=3`, `mem_total=4`, `docker_version=5`, `agent_version=6`, `type=7`, `available=8`. |
| `ListHostsResult` | `hosts=1`. |
| `ContainerInfo` | `id=1`, `name=2`, `image=3`, `command=4`, `created=5`, `started_at=6`, `finished_at=7`, `state=8`, `health=9`, `host_name=10`, `group=11`, `host_id=12`. |
| `ListContainersResult` | `containers=1`. |
| `ContainerStatEntry` | `id=1`, `name=2`, `host=3`, `cpu_percent=4`, `memory_percent=5`, `memory_usage=6`, `max_cpu_5min=7`, `max_memory_5min=8`, `host_id=9`, `network_rx_total=10`, `network_tx_total=11`, `network_rx_5min=12`, `network_tx_5min=13`. |
| `ContainerStatsResult` | `stats=1`. |
| `LogEntry` | `timestamp=1`, `message=2`, `stream=3`, `level=4`. |
| `FetchLogsResult` | `container_name=1`, `entries=2`. |
| `InspectContainerResult` | `id=1`, `name=2`, `image=3`, `command=4`, `created=5`, `started_at=6`, `finished_at=7`, `state=8`, `health=9`, `host_name=10`, `labels=11`, `memory_limit=12`, `cpu_limit=13`, tag/name `14/env` reserved, `ports=15`, `mounts=16`, `restart_policy=17`, `network_mode=18`, `host_id=19`. |
| `ActionResult` | `success=1`, `container_id=2`, `action=3`, `message=4`. |
| `DeployResult` | `success=1`, `project=2`, `message=3`. |
| `NotificationResult` | `success=1`, `message=2`. |
| `SearchLogsRequest` | `query=1`, `limit=2`, `before_ts_ns=3`, `host_id=4`, `container_id=5`. |
| `SearchLogsResponse` | `hits=1`, `has_more=2`, `next_before_ts_ns=3`. |
| `SearchLogHit` | `timestamp_ns=1`, `host_id=2`, `container_id=3`, `container_name=4`, `message=5`, `stream=6`, `level=7`, `log_id=8`. |

## FAZ 1 öncesi açık kararlar

Kaynak koddan çıkarılamayan ve istek metninde tanımlanmayan noktalar aşağıdadır. Bunlar onaylanmadan implementasyona geçilmemelidir.

1. **Sanal host ID:** `HostInfo.host.id` ne olmalı? Bu değer stabil ve diğer agent host ID'lerinden benzersiz olmak zorunda. Öneri: sabit `log-archive` (tek archive agent varsayımı). Container `host` alanları da aynı değer olur.
2. **Container `created` ve `started`:** İstek yalnız `SELECT DISTINCT svc` diyor, fakat Dozzle bu timestamp'leri geçmiş alt sınırı ve ilk stream başlangıcı olarak kullanıyor. Öneri: servis başına `MIN(ts)` cache'lemek ve hem `created` hem `started` için bunu kullanmak.
3. **İlk `StreamLogs` replay sınırı:** Dozzle ilk log ekranında `StreamLogs(since=started)` kullanır; yalnız gelecekteki satırları push etmek mevcut geçmişi ilk açılışta göstermez. Öneri: önce `since` sonrasındaki en yeni `MAX_ROWS_PER_QUERY` satırı kronolojik sırada replay etmek, sonra in-memory fan-out ile tail etmek. Bu sınırın tarih sorgusuyla aynı env'i kullanması onaylanmalı.
4. **Stream bilgisi:** Ingest sözleşmesinde `stream` alanı yoktur. Öneri: tüm loglarda `stream="stdout"`. Boş/unknown değer frontend'de stderr olarak yorumlanır.
5. **Raw download:** `StreamRawBytes` boş/no-op mu olsun, yoksa seçilen aralıktaki her `msg` değeri `\n` ile birleştirilerek gerçek indirme mi sağlansın? Öneri: gerçek indirme.
6. **Sonradan ortaya çıkan servis:** `StreamContainerStarted`, cache refresh sırasında yeni `svc` görülünce sanal Container event'i mi üretmeli? Öneri: evet; açık log/multi-container ekranlarının sayfa yenilemeden servisi görebilmesi için.

## FAZ 0 sonucu

Agent protokolü tahmin edilmemiş, `v10.6.14` kaynak kodundan doğrulanmıştır. Uygulamaya geçiş için yukarıdaki altı kararın onaylanması gerekir. Dozzle upgrade edildiğinde ilk kontrol edilmesi gereken dosyalar `protos/rpc.proto`, `protos/types.proto`, `internal/agent/server.go` ve `internal/agent/client.go` diff'leridir.
