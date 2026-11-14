# go-flake-hunter — Codex denetimi doğrulaması ve ortak çalışma planı

**Tarih:** 14 Kasım 2026 · **HEAD:** `0619b40` (`v0.3.0` + 5 commit)  
**Taraflar:** Codex denetimi (`~/.codex/visualizations/2026/11/13/.../go-flake-hunter-denetim.md`, T01–T20) ve Hermes denetimi (`~/Hermes/audits/go-flake-hunter-denetim-2026-11-14.md`, T1–T22).  
**Yöntem:** Codex'in her High iddiası, HEAD'den derlenmiş binary ile ayrı geçici modüllerde yeniden üretildi. Ana depo çalışma ağacı değişmedi (`git status` temiz).

---

## 1. Hüküm: Codex haklı mı?

**Evet, büyük ölçüde.** 20 bulgunun 17'si ya bizzat yeniden üretildi ya da kaynak + upstream belge ile doğrulandı. Üç konuda nüans var; hiçbiri iddianın özünü çürütmüyor.

| Codex | İddia | Benim verdict | Kanıt |
|---|---|---|---|
| T01 | Kapsül, üretilen Go koduna ifade enjekte ediyor | ✅ **Doğrulandı** | `label: 'TestVictim) ; t.Error("INJECTED") //'` → `compile --print` iki ifade bastı, `compile --verify` enjekte edileni çalıştırdı, exit 0 |
| T02 | Kapsül `id`'si depo dışına yazıyor/okuyor | ✅ **Doğrulandı** (okuma + yazma) | `id: "../../../escaped"` mevcut JSON'u ezdi; `explain ../secret` mağaza dışını okudu |
| T03 | CI logu checkout dışındaki pakette test çalıştırıyor | ✅ **Doğrulandı** | Logda `Package: "../outside"` → dış modülde `CANARY_RAN` oluştu, oraya partial kapsül yazıldı |
| T04 | Tanık dosyası sahipliği: kullanıcı dosyası siliniyor, symlink hedefi eziliyor | ✅ **Doğrulandı** | `zz_goflakehunter_witness_notes_test.go` silindi (hash öncesi alındı); symlink hedefi witness koduyla 216 bayta indi, link kaldırıldı |
| T05 | `compile --verify`, kurban geçse de polluter hatasını doğrulama sayıyor | ✅ **Doğrulandı** | Polluter FAIL + victim PASS → `verify` exit 0 "verified"; aynı kapsülde `replay` exit 3 "stale" |
| T06 | Scaffold, 2/3 yeniden üretimi yeşile çeviriyor | ✅ **Kaynak** | `replay` k/N → `ErrNotReproducible` → exit 3; `init` şablonu `3) exit 0` |
| T07 | Replay, aynı adlı testi yanlış modülde çalıştırıyor | ✅ **Doğrulandı** | Kapsül A modülünden, `--pkg .` B modülünde → ilgisiz FAIL "1/1 deterministic" |
| T08 | Action gizli dizin yüzünden kapsülleri yüklemiyor | ✅ **Kaynak + upstream** | `upload-artifact@v7` `include-hidden-files` **default `'false'`**; yol `**/.go-flake-hunter/*.json` |
| T09 | Paket düzeyi FAIL, testler PASS ise kayboluyor | ✅ **Doğrulandı** | `TestMain` sonrası `os.Exit(7)`: `go test` exit 1, JSON'da `TestOK pass` + paket `fail`; `hunt` exit 0 "flake not found" |
| T10 | Zaman bütçesi/iptal tüm işi kapsamıyor | ⚠️ **Kısmen** | Kilitte SIGINT takılması doğrulandı (benim T5); bütçe aşımı `budget.go`'da **bilinçli** yazılmış ("one run's own timeout"). Sözleşme dili düzeltilmeli, kod değil |
| T11 | CI tarifi `needs`/`failure` semantiği, eksik yol, geniş yetki | ✅ **Kaynak + upstream** | `if: needs.test.result == 'failure'` durum fonksiyonu içermez → örtük `success()` → iş atlanır; `./cmd/go-flake-hunter` tüketicide yok |
| T12 | Windows'ta kilit no-op, Unix'te sayaç sızıyor | ✅ **Kaynak** | `flock_windows.go` nil döner; `holders++` hata yolunda geri alınmıyor |
| T13 | Basılan order komutu temiz checkout'ta test çalıştırmıyor | ✅ **Doğrulandı** | `go test . -run '^TestFlakeWitness$'` → `[no tests to run]`, exit 0; `replay` aynı kapsülde 1/1 |
| T14 | Bayraklar kapsülde taşınmıyor; repeat N×N; `-race` iddiası | ✅ **Kaynak** | `--short`/`tags` kapsülde yok; gövde N tekrar + komut `-count=N`; `Race` yalnız "yeniden üretildi" demek |
| T15 | `--format json -v` JSON'u bozuyor; biçim geç doğrulanıyor | ✅ **Doğrulandı** | stdout'ta `written:` satırı → parser exit 1; `--format nonsense` önce tarayıp kapsül yazdı, sonra exit 2 |
| T16 | GOFLAGS etiketleri AST taramasında yok; toolchain kimliği karışık | ✅ **Kaynak** | `excludedByBuildTags` yalnız varsayılan etiketler; `tool.go` CLI'ın derlendiği sürüm |
| T17 | Büyük loglarda bellek/CPU | ⚠️ **Hipotez** | Kod okundu, ölçüm yok (benim T19 ile aynı) |
| T18 | Release kapısı/provenance/destek tablosu eksik | ✅ **Kaynak** | Tag üzerinde test şartı yok; `@latest`; SECURITY 0.1.x |
| T19 | Eşzamanlı baseline güncellemesi kayıt kaybediyor | ✅ **Kaynak** | Read–merge–write kilit altında değil |
| T20 | İki temizlik yolu + eski mimari dokümanı | ✅ **Kaynak** | `cli.cleanupWitnesses` + `harness.CleanupStale`; ARCHITECTURE ağacı eski |

### Nüanslar (anlaşmazlık değil, kalibrasyon)

1. **T10 "High"**: bütçe aşımı tasarım gereği (bir çalıştırma kadar taşma). Asıl hata kilit/iptal. Öneri: `--timeout` sert tavan olacaksa `context.WithTimeout` tüm işe; olmayacaksa README "soft, one run may overshoot" demeli. Şiddet: iptal **High**, bütçe **Medium**.
2. **T04 "High"**: katılıyorum — K2 ("asla kullanıcı dosyasını değiştirme") ihlali ve geri dönüşü yok.
3. **T01 "High"**: katılıyorum — kapsül paylaşımı ürünün özelliği; "güvenilmeyen kapsül" gerçekçi tehdit modeli.
4. Kapsam: Codex %84,2, ben %84,3 (yuvarlama); Codex Go 1.27.3, ben 1.26.2 (farklı toolchain).

### İki denetimin kör noktaları

| Codex'in kaçırdığı (Hermes buldu) | Hermes'in kaçırdığı (Codex buldu) |
|---|---|
| Windows'ta POSIX tek tırnak → `[no tests to run]` | Kapsül → Go ifade enjeksiyonu (T01) |
| Partial kapsülün "flake found"/SARIF "is flaky" render'ı | Log `Package` alanıyla checkout dışına çıkma (T03) |
| Baseline yaz/oku asimetrisi | Kullanıcı dosyası silme + symlink write-through (T04) |
| `repo.dirty`'nin kendi artefaktıyla kirlenmesi | Yanlış alt testin "verified" sayılması (T05) |
| README `explain gfh_*.json` çoklu kapsülde exit 2 | Scaffold 2/3 → yeşil (T06) |
| Env gate'in yanlış "no literal read" mesajı | Replay'in yanlış modülü seçmesi (T07) |
| Temiz taramada 500 satırlık çıktı gürültüsü | Gizli dizin → artefakt yüklenmemesi (T08) |
| `golangci-lint` 5 bulgu, CI'da lint yok | Paket düzeyi FAIL'in kaybolması (T09) |
| | Basılan komutun no-op olması (T13) |
| | `--format json -v` bozulması (T15) |
| | CI tarifi `needs`/`failure` semantiği (T11) |
| | GOFLAGS etiketleri (T16) |

**Sonuç:** İki rapor birbirinin alternatifi değil; birleşimi tek başına her iki raporun iki katı kapsama sağlıyor. Plan aşağıda bu birleşik defteri tek kaynak yapıyor.

---

## 2. Birleşik bulgu defteri (tek kaynak)

**Şiddet:** Critical yok. `✅` yeniden üretildi, `📖` kaynak/upstream, `⚠️` hipotez.  
**Sahip:** A = Codex lane'i (güven sınırı), B = Hermes lane'i (sonuç + sunum). Sahiplik **dosyaya** göre; bulan kişiye göre değil.

### Güvenlik / güven sınırı

| ID | Şiddet | Bulgu | Kaynak | Doğrulama | Sahip | Faz |
|---|---|---|---|---|---|---|
| S1 | High | Kapsül step adı → Go ifadesi enjeksiyonu | C-T01 | ✅ | A | P0 |
| S2 | High | Kapsül `id` okuma/yazma path traversal | C-T02, H-T7 | ✅ | A | P0 |
| S3 | High | `--from-json` `Package` alanı checkout dışına çıkıyor | C-T03 | ✅ | A | P0 |
| S4 | High | Tanık dosyası sahipliği: kullanıcı dosyası silme, symlink hedefi ezme | C-T04 | ✅ | A | P0 |
| S5 | High | Replay/compile hedef kimliği: yanlış modülde aynı adlı test; env anahtar uyuşmazlığı | C-T07 | ✅ | B (replay) + A (compile) | P0 |
| S6 | Medium | Windows komutlarında POSIX tek tırnak | H-T1 | ✅ simülasyon | B | P1 |
| S7 | Medium | Windows'ta süreçler arası kilit yok | C-T12, H-T11 | 📖 | B | P1 |

### Doğruluk / yanlış yeşil

| ID | Şiddet | Bulgu | Kaynak | Doğrulama | Sahip | Faz |
|---|---|---|---|---|---|---|
| C1 | High | `compile --verify` polluter hatasını doğrulama sayıyor | C-T05 | ✅ | A | P0 |
| C2 | High | Paket düzeyi FAIL (TestMain/teardown) testler PASS ise kayboluyor | C-T09 | ✅ | B | P0 |
| C3 | High | Scaffold 2/3 yeniden üretimi yeşile çeviriyor | C-T06 | 📖 | A | P1 |
| C4 | High | Sürekli kırmızı test "flake not found", hunt exit 0 | H-T2 | ✅ | B | P0 |
| C5 | High | `partial` kapsül "flake found" / SARIF "is flaky" olarak render ediliyor | H-T3 | ✅ | B | P0 |
| C6 | Medium | Basılan reproduce komutu no-op (`TestFlakeWitness` yok) | C-T13 | ✅ | B | P1 |
| C7 | High | SIGINT: keşifte exit 6, kilitte takılma; `holders` sızıyor | H-T4/H-T5, C-T10 | ✅ | B | P0 |
| C8 | Medium | `TestMain` `-list` sırasında çıkınca boş hata, exit 6 | H-T6 | ✅ | B | P0 |
| C9 | Medium | `--format json -v` JSON'u bozuyor; biçim geç doğrulanıyor | C-T15 | ✅ | B | P0 |
| C10 | Medium | `--short`/`tags` kapsülde yok; repeat N×N; `-race` iddiası | C-T14, H-T12 | 📖 | B | P1 |
| C11 | Medium | GOFLAGS etiketleri AST'de yok; `tool.go` yanlış toolchain | C-T16 | 📖 | B | P1 |
| C12 | Medium | Baseline yaz/oku asimetrisi (`--write-baseline` sonrası gate kırmızı) | H-T8 | ✅ | B | P1 |
| C13 | Medium | `repo.dirty`, aracın kendi `.go-flake-hunter/` çıktısıyla kirleniyor | H-T9 | ✅ | B | P1 |
| C14 | Low | Eşzamanlı baseline güncellemesi kayıt kaybediyor | C-T19 | 📖 | A | P1 |
| C15 | Medium | Bütçe sert tavan değil (bir çalıştırma taşar) | C-T10 | 📖 + ölçüm | B | P2 |

### Teslimat / CI

| ID | Şiddet | Bulgu | Kaynak | Doğrulama | Sahip | Faz |
|---|---|---|---|---|---|---|
| D1 | High | Action gizli dizin yüzünden hiç kapsül yüklemiyor | C-T08 | ✅ upstream default | A | P1 |
| D2 | High | CI-REPRO tarifi: `needs`+`failure` semantiği, eksik `./cmd/...`, geniş `pull-requests: write`, `--edit-last` işaretçisi | C-T11 | 📖 + docs | A | P1 |
| D3 | Medium | `@latest` scaffold; release gate/attestation/SBOM yok; SECURITY 0.1.x | C-T18, H-T20/H-T21 | 📖 | A | P1–P2 |
| D4 | Low | CI'da smoke/lint/govulncheck/coverage kapısı yok | H-T22 | 📖 | B | P0.0 |
| D5 | Low | README `explain gfh_*.json` çoklu kapsülde exit 2 | H-T10 | ✅ | B | P1 |
| D6 | Medium | Doküman drift'i: Jamf #474 merged, arch ağacı, "subtest reddedilir" | H-T13, C-T20 | ✅ + 📖 | B | P1 |

### Performans / kod sağlığı

| ID | Şiddet | Bulgu | Kaynak | Doğrulama | Sahip | Faz |
|---|---|---|---|---|---|---|
| P1 | Low | Büyük loglarda bellek/CPU | C-T17, H-T19 | ⚠️ | B | P2 |
| P2 | Low | Temiz taramada test başına 5 satır gürültü | H-T17 | ✅ | B | P2 |
| K1 | Low | Ölü kod (8+ sembol) | H-T14, C-T20 | 📖 | B | P2 |
| K2 | Low | `golangci-lint` 5 bulgu | H-T15 | ✅ | B | P0.0 |
| K3 | Low | İki temizlik yolu | C-T20 | 📖 | A | P0 (S4 ile birleşir) |
| K4 | Low | Env gate'in yanlış "no literal read" mesajı | H-T16 | ✅ | B | P2 |
| K5 | Low | "En yeni" kapsül mtime ile seçiliyor; clone'da eşitlenir | H-T18 | ⚠️ | B | P2 |

**Toplam:** 5 High güvenlik, 6 High doğruluk, 2 High teslimat = **13 High**; Critical yok.

---

## 3. Çakışma önleme: tek yazar kuralı

İki ajanın çakışma riski üç dosyada toplanıyor: `internal/cli/compile.go`, `internal/compile/persistent.go`, `internal/cli/hunt.go`. Kural:

1. **Her dosyanın her fazda tek sahibi var.** Diğer lane o dosyaya dokunmaz; değişiklik gerekiyorsa PR gövdesine `handoff:` notu yazar, sahibi uygular.
2. **Sözleşme tek sahibi:** `internal/domain/errors.go` (exit kodları), `internal/domain/capsule.go` (şema alanları) ve `README` exit-code tablosu **yalnız B**. A yeni kod/alan isterse B'ye handoff.
3. **Ortak yardımcı önce iner:** import-path çözümü `internal/gotest` (B) içinde tek fonksiyon; A bunu `compile.go`'da çağırır. B bu yardımcıyı P0'ın ilk PR'ında merge eder.
4. **Rebase penceresi:** her merge sonrası diğer lane `git pull --rebase`; çakışma varsa **dosya sahibi** çözer, diğeri değil.
5. **Aynı anda tek faz:** P0 bitmeden P1 dosyalarına dokunulmaz. Paralellik lane'ler arasında, fazlar arasında değil.

### Dosya sahipliği matrisi

| Dosya / dizin | P0 | P1 | P2 |
|---|---|---|---|
| `internal/capsule/**` | A | A | A |
| `internal/domain/witness.go` | A | A | A |
| `internal/domain/capsule.go`, `errors.go`, `run.go`, `test.go` | B | B | B |
| `internal/harness/**` | A | A | A |
| `internal/cli/witness_cleanup.go` | A (K3 ile silinir) | — | — |
| `internal/gotest/patterns.go`, `internal/cli/repro.go` | A | A | A |
| `internal/cli/compile.go` | A (S2, C1, hedef kimliği) | B (C6 komut satırı) | B |
| `internal/compile/persistent.go` | A (S1 doğrulama) | B (C10 tekrar/bayrak) | B |
| `internal/gotest/{runner,json,discover,cache}.go` | B | B | B |
| `internal/probe/**` | B | B | B |
| `internal/policy/**` | B | B | B |
| `internal/cli/{hunt,replay,cli}.go` | B | B | B |
| `internal/report/**` | B | B | B |
| `internal/cli/init.go`, `action.yml`, `.github/workflows/**` | A (P1) | A | A |
| `docs/CI-REPRO.md`, `SECURITY.md` | — | A | A |
| `README.md`, `docs/ARCHITECTURE.md`, `CHANGELOG.md` | B | B | B |
| `Makefile`, `CONTRIBUTING.md` | B (P0.0) | B | B |

`internal/cli/hunt.go` B'de kalıyor çünkü C2/C4/C5/C9/C13 ve C6'nın yarısı orada; A'nın tek ihtiyacı `S2` için `capsuleDir` — o zaten `capsule` paketinde.

---

## 4. Faz planı

Süreler tek geliştirici iş günü; iki lane paralel çalıştığı için takvim süresi ≈ en uzun lane.

### P0.0 — Güvenlik ağı (0,5 gün, B, ilk PR)

| İş | Dosya | Kabul |
|---|---|---|
| CI'a `golangci-lint`, `make smoke`, `govulncheck`, coverage raporu | `ci.yml`, `Makefile` | Mevcut seviyede yeşil; K2 düzeltilir |
| `make test-short` ayrımı + CONTRIBUTING düzeltmesi | `Makefile`, `CONTRIBUTING.md` | `make test` "fast unit" demiyor |
| Ortak import-path yardımcısı | `internal/gotest` | A tek fonksiyonu çağırıyor; testli |

### P0 — "Yanlış yeşil ve kod çalıştırma" (5–8 gün)

**Lane A (Codex) — güven sınırı**

| İş | Bulgu | Dosya | Kabul kriteri |
|---|---|---|---|
| A1 Kapsül doğrulama: step adı gerçek test bildirimi + clause eşleşmesi; `id` regex + `filepath.Base`; okuma/yazma tek doğrulanmış anahtar | S1, S2 | `capsule/store.go`, `domain/witness.go`, `compile/persistent.go` | 6 negatif fixture (statement, traversal, yanlış package, symlink, legacy `fs_*`, bozuk şema) child process/dış yazı **öncesi** reddedilir; legacy kapsüller okunur |
| A2 Log hedefi: `ImportPath → Dir` haritası checkout'tan; symlink realpath kontrolü | S3 | `gotest/patterns.go`, `cli/repro.go` | `../outside` fixture reddedilir; gerçek çoklu modül senaryosu çalışır |
| A3 Tanık sahipliği: `O_CREATE\|O_EXCL`, sahiplik işareti, symlink reddi, tek temizleme yolu | S4, K3 | `harness/install.go`, `cli/witness_cleanup.go` (silinir) | Kullanıcı dosyası hash'i ve symlink hedefi değişmez; killed-run artığı temizlenir |
| A4 Verify kararı: kurban alt testi (order/parallel/env), beklenen tur (repeat), run tamamlandı mı | C1 | `cli/compile.go` | Polluter FAIL + victim PASS → **verified değil**; victim FAIL → verified |
| A5 Compile hedef kimliği: import yolu + kök doğrulaması | S5 (compile yarısı) | `cli/compile.go` | Yanlış modülde aynı adlı test reddedilir |

**Lane B (Hermes) — sonuç ve sunum sözleşmesi**

| İş | Bulgu | Dosya | Kabul kriteri |
|---|---|---|---|
| B1 Merkezi yürütme durumu: süreç sonu → paket sonu → beklenen test sonu; `broken`/`incomplete`/`package-failed` statüleri; JSON alanları | C2, C4 | `gotest/runner.go`, `json.go`, `probe/repeat.go`, `cli/hunt.go`, `domain/run.go` | `TestMain` exit 7 fixture: exit 0 değil, "package failed" görünür; always-fail test "broken" |
| B2 Partial renderer'ları: her formatta "inconclusive", SARIF `inconclusive` rule, `Target` alanları | C5 | `report/*.go`, `cli/hunt.go` | 4 format + `explain` partial testi |
| B3 İptal: `ctx.Err()` keşifte önce; `LOCK_NB` + ctx döngüsü; `holders` hata yolunda geri | C7, C8 | `gotest/discover.go`, `policy/dirlock.go`, `flock_*` | SIGINT keşifte 130; kilitte <1 s; hata sonrası ikinci kilit gerçekten alınır |
| B4 Makine çıktısı: `-v` → stderr; `--format` parse sonrası hemen doğrulanır | C9 | `cli/hunt.go`, `cli/cli.go` | `--format json -v` parse edilir; `--format nonsense` hiç run harcamaz |
| B5 Replay kimliği: kapsül import yolu + kök; `--pkg` uyuşmazlığı hata; env anahtarı denetimi | S5 (replay yarısı) | `cli/replay.go` | Yanlış modül fixture'ı reddedilir; `--env OTHER=` uyarı |

**P0 çıkış kapısı:** 11 negatif fixture üç OS'te yeşil; `make test`, `make fixtures`, `make smoke`, `lint`, `govulncheck` yeşil; legacy kapsül uyumu korunmuş.

### P1 — "CI doğrusu ve kopyalanabilir komut" (≈2 hafta)

| İş | Bulgu | Sahip | Dosya | Kabul |
|---|---|---|---|---|
| A6 Artefakt: kapsülleri görünür staging dizinine kopyala; `if-no-files-found: error`; matrix'te benzersiz ad | D1 | A | `action.yml`, `init.go`, `ci.yml`, `nightly.yml`, `CI-REPRO.md` | 1 kapsül üret → 1 kapsül indir; iç PR + fork PR |
| A7 Scaffold kapısı: 0/N yeşil, k/N kırmızı, altyapı "unknown"; `failure()` semantiği | C3, D2 | A | `init.go`, `docs/CI-REPRO.md` | 2/3 fixture kırmızı; fork PR'da yazma tokenı yok |
| A8 Release: test edilmiş SHA, sabit toolchain, attestation/SBOM, SECURITY tablosu | D3 | A | `release.yml`, `SECURITY.md` | Beş build + provenance doğrulanır |
| B6 Basılan komut: `replay <capsule> -n 1`; `compile.CommandLine` gerçek kapsül yolu; `--short`/tags kapsülde; repeat tek katman | C6, C10 | B | `cli/compile.go`, `compile/persistent.go`, `cli/hunt.go`, `domain/capsule.go` | Kopyalanan komut 4 sınıfta da çalışır; N×N yok |
| B7 Baseline: varsayılan dosya otomatik yüklenir; action girdileri; atomik birleşim kilidi | C12, C14 | B | `cli/hunt.go`, `capsule/baseline.go`, `action.yml`? (A'ya handoff) | Yaz→gate yeşil; paralel iki yazıda kayıt kaybı yok |
| B8 Provenance: `repo.dirty` kendi çıktısını hariç tutar | C13 | B | `cli/git.go` | İki ardışık hunt `dirty:false` |
| B9 GOFLAGS/tags: aktif dosya kümesi `go list -json`'dan; iki toolchain kimliği | C11 | B | `facts/scan.go`, `cli/hunt.go` | `GOFLAGS=-tags=x` fixture'ı bulunur |
| B10 Doküman: Jamf merged, arch ağacı, "subtest" ifadesi, README glob | D5, D6 | B | `README.md`, `ARCHITECTURE.md`, `CHANGELOG.md` | docs-drift + manuel kontrol |

### P2 — "Ölçüm ve sertleştirme" (pilot sonrası)

| İş | Bulgu | Sahip | Kabul |
|---|---|---|---|
| B11 Streaming/bellek: 50 paket + gürültülü log ölçümü, sonra karar | P1 | B | p95 süre + peak RSS başlangıç ölçümü; recall kaybı yok |
| B12 Bütçe sözleşmesi: sert tavan mı, "bir run taşar" mı — kod veya doküman | C15 | B | README + test aynı şeyi söyler |
| B13 Çıktı gürültüsü: temiz taramada tek satır özet | P2 | B | 100 testte <10 satır |
| B14 Ölü kod + env gate mesajı | K1, K4 | B | `unused` temiz; mesaj "read but no literal value" |

---

## 5. Çalışma protokolü (ikisi için bağlayıcı)

1. **Branch:** `audit/p0-a-trust`, `audit/p0-b-outcome`, `audit/p1-a-ci`, `audit/p1-b-contract`… Her PR tek bulgu ailesi.
2. **Red-before-fix kanıtı:** her regresyon testi, fix stash'lenmiş hâlde kırmızı olduğu gösterilmeden merge edilmez. PR gövdesinde test adı + önce/sonra çıktısı.
3. **Test adlandırma:** `TestAudit<SID>_<senaryo>` (ör. `TestAuditS1_RejectsStatementInStepLabel`).
4. **Sözleşme değişikliği:** yeni exit kodu / JSON alanı / SARIF rule → B'nin PR'ında, README tablosu + CHANGELOG aynı PR'da.
5. **Handoff:** diğer lane'in dosyasında değişiklik gerekiyorsa PR gövdesine:
   `handoff: <dosya>:<satır> — <ne>: <neden>`; sahibi kendi PR'ında uygular.
6. **Rebase:** merge sonrası `git pull --rebase`; çakışmayı dosya sahibi çözer.
7. **Kapı:** `make test`, `make fixtures`, `make smoke`, `golangci-lint run`, `govulncheck ./...`, `scripts/docs-drift.sh` — hepsi yeşil olmadan merge yok.
8. **Ölçüm:** P0 ve P1 sonunda üç OS CI + (varsa) yerel Windows; P0 çıkışında 11 negatif fixture matrisi raporlanır.
9. **Doküman:** davranış değişikliği README/ARCHITECTURE/CHANGELOG'a aynı PR'da; docs-drift genişletilir (SECURITY sürümü, arch ağacı, PR durumu manuel).

---

## 6. Açık kararlar (insan onayı gereken)

| # | Karar | Seçenekler | Öneri |
|---|---|---|---|
| 1 | Kapsül güven modeli | (a) kapsül "güvenilmez veri" — tam doğrulama; (b) "güvenilir ekip artefaktı" — yalnız uyarı | (a); paylaşım özelliği varsa (a) zorunlu |
| 2 | `--timeout` sözleşmesi | (a) sert tavan; (b) "bir run taşabilir" dokümanı | (a) P2'de, çünkü CI'da 10m vaadi var |
| 3 | Scaffold 2/3 | (a) kırmızı; (b) "unknown" exit 4 | (a) — gate'in işi flake'i göstermek |
| 4 | Windows desteği | (a) tam parite (kilit + komut + native CI); (b) "best-effort" olarak ilan | (a) P1'de; (b) yalnız süre yoksa |
| 5 | Release hızı | (a) P0 bitince güvenlik patch'i (v0.3.1); (b) P1 sonunda v0.4.0 | (a) + (b); güvenlik ayrı yayınlanır |

---

## 7. Özet

- Codex'in 20 bulgusundan **18'i doğrulandı** (yeniden üretim veya kaynak+upstream), **2'si kısmen/hipotez** (T10 kısmen, T17 hipotez); **hiçbiri yanlış değil**. Şiddet kalibrasyonu iki yerde tartışmalı (T10, T04) ama öz aynı.
- Hermes'in 22 bulgusundan Codex'te karşılığı olmayan **12'si** birleşik deftere girdi; Codex'te olup Hermes'te olmayan **12** bulgu da.
- Çakışma riski üç dosyada; **tek yazar + faz sırası + handoff** kuralıyla sıfırlanıyor.
- İlk iş **P0.0 güvenlik ağı** (B), sonra paralel **P0-A güven sınırı** ve **P0-B sonuç sözleşmesi**. P0 çıkışı 11 negatif fixture + üç OS yeşili.
- Ürün kararı: P0 bitince **v0.3.1 güvenlik patch'i**; P1 bitince **v0.4.0**.
