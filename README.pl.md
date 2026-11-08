<p align="center">
  <img src="docs/logo.svg" width="64" alt="ffnvr logo">
</p>

# ffnvr

[English](README.md) | **Polski**

**ffnvr** to rejestrator wideo (NVR) dla Linuksa, konfigurowany składnią wzorowaną na MikroTik RouterOS. Nagrywa kamery IP za pomocą `ffmpeg`, każdą kamerę uruchamia jako osobną usługę `systemd`, a do tego potrafi zarządzać zdalnymi rejestratorami sprzętowymi (Dahua, Uniview, Tiandy, XMeye/DVRIP): odczytywać ich kamery i stan, pobierać nagrania, restartować je, robić kopie konfiguracji, ustawiać zegar i odbierać zdarzenia.

Cała konfiguracja to jeden skrypt tekstowy (domyślnie `/etc/ffnvr/config.rsc`), edytowany w interaktywnej powłoce z dopełnianiem klawiszem Tab i pomocą kontekstową – tak jak w konsoli RouterOS.

![ffnvr](docs/ffnvr-main.png)

---

## Spis treści

- [Możliwości](#możliwości)
- [Jak to działa](#jak-to-działa)
- [Wymagania](#wymagania)
- [Instalacja](#instalacja)
- [Pierwsze kroki](#pierwsze-kroki)
- [Panel WWW](#panel-www)
- [Opis CLI](#opis-cli)
  - [Polecenie `ffnvr`](#polecenie-ffnvr)
  - [Powłoka interaktywna](#powłoka-interaktywna)
  - [Składnia skryptu konfiguracji](#składnia-skryptu-konfiguracji)
  - [Opis menu](#opis-menu)
- [Pliki i katalogi](#pliki-i-katalogi)
- [Rozwój](#rozwój)

---

## Możliwości

- **Nagrywanie lokalne** kamer IP po RTSP przez `ffmpeg -c copy` (bez ponownego kodowania), w 15-minutowych segmentach `.mkv`.
- **Jedna usługa systemd na kamerę** (`ffnvr-<nvr>-<kamera>.service`), z opcjonalną izolacją (`/system/systemd security=basic|high|custom`) i limitem pamięci.
- **Zdalne rejestratory**: Dahua (JSON-RPC lub CGI), Uniview, Tiandy i XMeye/DVRIP. Kamery pojawiające się na rejestratorze mogą być dodawane automatycznie.
- **Retencja i rezerwa dysku**: stare nagrania są usuwane po zadanym czasie lub gdy brakuje wolnego miejsca.
- **Harmonogramy nagrywania**: szablony dni i harmonogramy tygodniowe z trybami `regular`, `alarm` (zachowywane tylko otoczenie alarmów, z czasem przed i po alarmie) oraz `none`.
- **Serwer zdarzeń/alarmów**: serwer alarmów DVRIP, serwer SMTP dla e-maili z rejestratorów, odbiornik Uniview LAPI oraz subskrypcje urządzeń Dahua, Hikvision, Tiandy, Uniview i DVRIP. Zdarzenia są przechowywane w SQLite, filtrowane i potwierdzane.
- **Zadania archiwizacji**: eksport nagrań (lokalnych lub ze zdalnych rejestratorów) do `.mp4`, opcjonalnie w blokach.
- **Paski ruchu** w archiwum, wyliczane z lokalnych nagrań.
- **Sterowanie PTZ** kamer, również tych podłączonych do rejestratorów.
- **Podgląd na żywo** przez [go2rtc](https://github.com/AlexxIT/go2rtc) (opcjonalnie), z transkodowaniem do H.264 na żądanie dla przeglądarek bez H.265.
- **Użytkownicy, grupy i polityki** ze szczegółowymi uprawnieniami do panelu WWW.
- **Bezpieczne zmiany**: `diff` pokazuje dokładnie, które pliki unitów i wywołania `systemctl` spowoduje zmiana, zanim zrobi to `commit`/`apply`.
- Jeden statyczny plik binarny w Go (`CGO_ENABLED=0`).

## Jak to działa

ffnvr nie działa jako jeden duży demon. Konfiguracja jest zamieniana na zestaw unitów `systemd`, a `apply` (lub `commit` w powłoce) zapisuje je i uruchamia, zatrzymuje lub restartuje tylko to, co się zmieniło.

| Unit | Kiedy istnieje | Co robi |
|---|---|---|
| `ffnvr-<nvr>-<kamera>.service` | każda włączona kamera rejestratora `type=local` | `ffmpeg` nagrywający kamerę do `<storage>/<nvr>/<kamera>/` |
| `ffnvr-worker.service` | zawsze | `ffnvr worker`: zadania archiwizacji, retencja, harmonogramy, odpytywanie zdalnych rejestratorów, automatyczne dodawanie kamer, miniatury, zrzuty |
| `ffnvr-www.service` | włączona usługa `/ip/service www` | `ffnvr web`: panel WWW |
| `ffnvr-alarm.service` | włączona usługa `alarm-*` lub dowolne `events=subscribe` | `ffnvr alarm`: serwer zdarzeń |
| `ffnvr-go2rtc.service` | `/system/live enabled=yes` | go2rtc do podglądu na żywo, nasłuchujący tylko na localhost |

Usługi czytają ten sam plik konfiguracji, więc większość zmian zapisanych w powłoce działa bez ich restartu.

![How ffnvr works](docs/how-it-works-pl.png)

## Wymagania

- Linux z **systemd**.
- **ffmpeg** w `/usr/bin/ffmpeg` (usługi kamer wywołują go po tej ścieżce).
- **Go 1.22+** do kompilacji.
- Opcjonalnie: **go2rtc** (domyślnie `/usr/local/bin/go2rtc`) do podglądu na żywo i miniatur.
- Uprawnienia roota do instalacji unitów i uruchomienia `apply`.

## Instalacja

```bash
git clone https://github.com/maciejkaczkowski/ffnvr.git ffnvr
cd ffnvr

go get modernc.org/sqlite@v1.34.3

make build          # buduje statyczny plik ./ffnvr
make test           # opcjonalnie: go vet + testy
sudo make install   # patrz niżej
```

`make install`:

- instaluje program w **`/usr/local/bin/ffnvr`** (wygenerowane unity uruchamiają go z tej ścieżki, więc musi tam zostać),
- tworzy katalog `/etc/ffnvr`,
- tworzy konto systemowe `ffnvr`, używane przez usługi kamer przy `/system/systemd security=high`.

Zainstaluj ffmpeg (i opcjonalnie go2rtc), np. w Debianie/Ubuntu:

```bash
sudo apt install ffmpeg
# opcjonalnie, do podglądu na żywo:
sudo install -m 0755 go2rtc_linux_amd64 /usr/local/bin/go2rtc
```

Aby wbudować numer wersji w program:

```bash
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=1.0.0" -o ffnvr ./cmd/ffnvr
```

## Pierwsze kroki

### 1. Utwórz plik konfiguracji i otwórz powłokę

```bash
sudo touch /etc/ffnvr/config.rsc
sudo ffnvr shell
```

```
ffnvr shell - Tab completes, ? shows help, Ctrl-D leaves
[ffnvr] />
```

W każdej chwili możesz nacisnąć `Tab`, aby dopełnić menu, parametry i wartości, albo wpisać `?`, aby zobaczyć, co jest w danym miejscu dozwolone.

### 2. Dodaj magazyn na nagrania

```
/storage add name=disk1 path=/srv/disk1 types=archive,backup
```

### 3. Dodaj rejestrator lokalny

```
/nvr add name=nvr01 type=local storage=disk1 retention=30d
```

### 4. Dodaj kamery

Bezpośrednio, adresem RTSP:

```
/camera add name=yard nvr=nvr01 stream="rtsp://user:pass@192.168.1.21:554/stream1" alias=Podworko
```

Albo przez wspólny profil (login i schemat adresu dla wielu kamer tego samego producenta):

```
/camera/profile add name=dahua type=dahua username=admin password=secret \
    stream="rtsp://<user>:<password>@<host>/cam/realmonitor?channel=1&subtype=0"
/camera add name=front nvr=nvr01 profile=dahua host=192.168.1.20
```

`print detail` pokazuje wartości, które kamera dziedziczy z profilu, w tym automatycznie wyznaczony strumień niskiej jakości:

```
[ffnvr*] /> /camera print detail
  0  name=front nvr=nvr01 profile=dahua host=192.168.1.20 enabled=yes
       username=admin  (profile dahua)
       password=*****  (profile dahua)
       stream=rtsp://admin:*****@192.168.1.20/cam/realmonitor?channel=1&subtype=0  (profile dahua)
       stream-lq=rtsp://admin:*****@192.168.1.20/cam/realmonitor?channel=1&subtype=1  (made from stream)
       engine=internal  (default)
```

### 5. (Opcjonalnie) Użytkownik i panel WWW

```
/user/policy add name=admin allow=*
/user add user=admin policy=admin password=ZmienMnie
/ip/service set www enabled=yes port=8080
```

Hasło jest zapisywane wyłącznie jako skrót SHA-512 crypt (`password-hash=`).

### 6. Sprawdź i zastosuj

```
diff      # pokazuje pliki unitów i wywołania systemctl, które wykona commit
commit    # zapisuje unity, uruchamia/restartuje usługi, zapisuje konfigurację
```

Gwiazdka w znaku zachęty (`[ffnvr*]`) oznacza niezatwierdzone zmiany. Sprawdź wynik:

```bash
systemctl status 'ffnvr-*'
ls /srv/disk1/nvr01/front/
```

### Dodanie zdalnego rejestratora

```
/nvr add name=nvr02 type=dahua host=192.168.1.2 username=admin password=secret
/nvr scan nvr02     # odczytuje rejestrator i dodaje kamerę dla każdego aktywnego kanału
/nvr probe nvr02    # surowe odpowiedzi rejestratora, do diagnostyki
```

Kamery zdalnego rejestratora dostają nazwy `chNN` (numer kanału) i alias z nazwy kanału. Przy `auto-add=yes` (domyślnie) worker sam dodaje nowe kanały.

### Praca bez powłoki

Konfigurację można też edytować w zwykłym edytorze tekstu i stosować z linii poleceń:

```bash
sudo ffnvr diff               # co się zmieni
sudo ffnvr apply              # zastosuj
ffnvr export                  # wypisz znormalizowaną konfigurację
```

## Panel WWW

ffnvr zawiera panel WWW (`ffnvr web`, włączany przez `/ip/service set www enabled=yes`). Można go uruchomić i używać, ale **powstał głównie w celu podglądu, a nie konfiguracji**: podgląd kamer na żywo, przeglądanie i odtwarzanie archiwum, dziennik zdarzeń oraz przegląd stanu rejestratorów, dysków i zadań. Dostęp określają `/user`, `/user/group` i `/user/policy`.

Konfigurację ffnvr wykonuje się w CLI opisanym poniżej. Panel jest dostępny po angielsku, polsku i niemiecku.

---

## Opis CLI

### Polecenie `ffnvr`

```
ffnvr <polecenie> [flagi]     (flagi mogą stać także przed poleceniem)
```

Bez polecenia ffnvr wypisuje tylko pomoc: `apply` trzeba wywołać jawnie.

#### Polecenia

| Polecenie | Opis |
|---|---|
| `shell` | Powłoka interaktywna (patrz [niżej](#powłoka-interaktywna)). |
| `apply` | Zapisuje unity systemd wynikające z konfiguracji i uruchamia/zatrzymuje/restartuje je. |
| `diff` | Wypisuje, co zmieniłoby `apply`; niczego nie modyfikuje. |
| `export` | Wypisuje znormalizowaną konfigurację. |
| `worker` | Wykonuje zadania archiwizacji i prace okresowe, sprawdza zdalne rejestratory (`ffnvr-worker.service`). |
| `alarm` | Serwer alarmów: zdarzenia rejestratorów i kamer (`ffnvr-alarm.service`). |
| `web` | Panel WWW (`ffnvr-www.service`). |
| `help` | Wypisuje pomoc. |

`worker`, `alarm` i `web` są zwykle uruchamiane przez wygenerowane unity systemd, nie ręcznie.

#### Flagi

| Flaga | Domyślnie | Dotyczy | Opis |
|---|---|---|---|
| `-c <ścieżka>` | `/etc/ffnvr/config.rsc` | wszystkie | Plik konfiguracji lub katalog plików `*.rsc`. Można podać wielokrotnie. |
| `-root <katalog>` | `/` | wszystkie | Działa na podanym korzeniu systemu plików. `systemctl` jest uruchamiany tylko gdy to `/` (przydatne do testów). |
| `-no-services` | `false` | apply, shell | Zapisuje pliki unitów, ale nie uruchamia `systemctl`. |
| `-history <plik>` | `~/.ffnvr_history` | shell | Plik historii poleceń (pusty: tylko w pamięci). |
| `-ffmpeg <program>` | `ffmpeg` | worker | Program ffmpeg używany przez workera. |
| `-status-every <czas>` | `1m` | worker | Jak często worker sprawdza, czy jest coś do zrobienia. |
| `-session-keep <czas>` | `24h` | worker | Sesje panelu WWW nieużywane tak długo są usuwane. |
| `-listen <adres>` | `:80` | web | Adres nasłuchu. |
| `-allow <lista>` | *(wszyscy)* | web | Adresy/sieci, które mogą się łączyć, np. `192.168.0.0/16,10.0.0.5`. |
| `-proxy <lista>` | | web | Reverse proxy, którym wierzy się w nagłówku `X-Forwarded-For`. |
| `-dvrip <adres>` | *(wył.)* | alarm | Adres serwera alarmów DVRIP, np. `:15002`. |
| `-dvrip-allow <lista>` | *(wszyscy)* | alarm | Kto może wysyłać alarmy DVRIP. |
| `-smtp <adres>` | *(wył.)* | alarm | Adres serwera SMTP, np. `:2525`. |
| `-smtp-allow <lista>` | *(wszyscy)* | alarm | Kto może wysyłać e-maile. |
| `-lapi <adres>` | *(wył.)* | alarm | Adres serwera HTTP, do którego urządzenia Uniview wysyłają zdarzenia (LAPI), np. `:8091`. |
| `-lapi-allow <lista>` | *(wszyscy)* | alarm | Kto może wysyłać zdarzenia LAPI. |

Przykłady:

```bash
ffnvr shell
ffnvr diff   -c /etc/ffnvr/config.rsc
ffnvr apply  -c /etc/ffnvr/config.rsc
ffnvr export -c /etc/ffnvr/conf.d/          # katalog plików *.rsc
ffnvr diff   -root /tmp/test -c ./test.rsc  # sprawdzenie konfiguracji bez dotykania systemu
```

Polecenia `shell` i `web` mogą zapisywać zmiany z powrotem do konfiguracji tylko wtedy, gdy podano dokładnie jeden plik (nie katalog).

### Powłoka interaktywna

`ffnvr shell` otwiera konsolę w stylu RouterOS. Zmiany są trzymane w pamięci aż do `commit`.

W terminalu dostępna jest edycja linii, trwała historia, dopełnianie klawiszem `Tab` (menu, parametry, wartości, nazwy elementów) i pomoc kontekstowa `?`. Gdy wejście pochodzi z potoku lub pliku, powłoka czyta zwykłe linie, więc można ją skryptować:

```bash
printf '/camera print\n' | ffnvr shell
```

#### Nawigacja

| Wpis | Efekt |
|---|---|
| `/camera` | Przejście do menu `/camera`. |
| `/camera profile` lub `/camera/profile` | Przejście do podmenu. |
| `..` | Poziom wyżej. |
| `/` | Powrót do korzenia. |
| `/camera print` | Wykonanie polecenia w innym menu bez przechodzenia do niego. |

Znak zachęty pokazuje bieżące menu: `[ffnvr] /camera>`.

#### Polecenia powłoki

| Polecenie | Opis |
|---|---|
| `diff` | Pokazuje, co zmieni `commit` (unity systemd, wywołania systemctl). |
| `commit` | Zapisuje unity, uruchamia/zatrzymuje/restartuje usługi, zapisuje konfigurację. |
| `save [plik]` | Zapisuje konfigurację jako skrypt (domyślnie: wczytany plik). |
| `load <plik>` | Nakłada skrypt lub katalog na bieżący stan. |
| `history` | Pokazuje historię poleceń. |
| `help` | Krótka pomoc. |
| `quit`, `exit`, `Ctrl-D` | Wyjście z powłoki. |
| `<tekst>?` | Pomoc kontekstowa do tego, co już wpisano. |

Linie zawierające hasła nigdy nie trafiają do historii.

#### Klawisze

| Klawisz | Działanie |
|---|---|
| `Tab` | Dopełnia menu, parametry, wartości i nazwy elementów. |
| `?` | Pomoc dla bieżącego miejsca. |
| `Góra` / `Dół` | Przeglądanie historii. |
| `Ctrl-R` | Wyszukiwanie w historii. |
| `Ctrl-A` / `Ctrl-E` | Początek / koniec linii. |
| `Ctrl-K` / `Ctrl-U` | Usunięcie do końca / początku linii. |
| `Ctrl-W` | Usunięcie poprzedniego słowa. |
| `Ctrl-Y` | Wklejenie usuniętego tekstu. |
| `Alt-B` / `Alt-F` | Słowo wstecz / naprzód. |

#### Polecenia menu (czasowniki)

| Czasownik | Opis |
|---|---|
| `print` | Pokazuje elementy. `print detail` pokazuje też wartości dziedziczone i bieżący stan (kamer, rejestratorów…). |
| `add k=v ...` | Tworzy element (menu listowe). |
| `set <element> k=v ...` | Zmienia elementy. W menu pojedynczych (np. `/system/settings`): `set k=v`. |
| `unset <element> k ...` | Usuwa wartości (powrót do domyślnych). |
| `remove <element>` | Usuwa elementy. Elementów, do których odwołuje się coś innego, nie da się usunąć. |
| `enable <element>` / `disable <element>` | Skrót dla `disabled=no/yes` lub `enabled=yes/no`. |
| `export` | Wypisuje konfigurację tego menu albo wszystkiego poniżej bieżącego poziomu. |

Czasowniki specyficzne dla menu (`scan`, `probe`, `rights`, `status`, `cancel`, `ack`, `reboot`, `backup`, `upgrade`, `datetime`) są opisane w [opisie menu](#opis-menu).

#### Wybór elementów

| Selektor | Przykład |
|---|---|
| Numer (jak w `print`) | `set 0 enabled=no` |
| Kilka numerów | `/event/log ack 12,13` |
| Nazwa | `set front enabled=no` |
| Alias | `set Podworko enabled=no` |
| `[find k=v ...]` | `remove [find comment="stare"]`, `set [find name=front nvr=nvr01] ptz=yes` |

Nazwy kamer są unikalne tylko w obrębie rejestratora. Gdy ta sama nazwa występuje na kilku rejestratorach, użyj numeru albo `[find name=... nvr=...]`.

### Składnia skryptu konfiguracji

Plik konfiguracji jest skryptem w tej samej składni co powłoka; tworzą go `export` i `save`:

```
# komentarz
/storage
add name=disk1 path=/srv/disk1 types=archive,backup

/camera
add name=front nvr=nvr01 profile=dahua host=192.168.1.20 \
    comment="wejście główne"

/ip/service
set [ find name=www ] port=8080 enabled=yes
```

- Linia zawierająca tylko ścieżkę menu zmienia bieżące menu; ścieżka może też poprzedzać polecenie w tej samej linii (`/system/live set enabled=yes`).
- `#` rozpoczyna komentarz, `\` na końcu linii ją kontynuuje.
- Wartości zawierające spacje lub któryś ze znaków `" \ $ [ ] ( ) ; # ?` trzeba ująć w cudzysłów; wewnątrz działają sekwencje `\"`, `\\`, `\$` i `\n`.
- Wartości logiczne: `yes`/`no` (także `true`/`false`). Listy: oddzielone przecinkami `a,b,c`.
- Czasy: `10s`, `5m`, `1h`, `30d`. Rozmiary: `200M`, `2G`.
- Katalog podany w `-c` jest wczytywany jako wszystkie zawarte w nim pliki `*.rsc`.

### Opis menu

Legenda: **wym.** = wymagany, **tajny** = maskowany przez `print`, **ref** = musi wskazywać istniejący element innego menu.

#### `/storage`

Miejsca na nagrania i archiwa. Menu listowe, nazwa elementu: `name`.

| Parametr | Typ | Opis |
|---|---|---|
| `name` | tekst, wym. | Nazwa używana w `/nvr storage=...`. |
| `path` | tekst, wym., unikalny | Ścieżka bezwzględna, np. `/srv/disk1`. |
| `types` | lista | Do czego jeszcze służy: `archive` (strona Archiwum), `backup` (kopie konfiguracji). |
| `disabled` | yes\|no | |
| `comment` | tekst | |

#### `/user/group`

Grupy użytkowników (np. jedna na firmę). Menu listowe, nazwa elementu: `group`.

| Parametr | Typ | Opis |
|---|---|---|
| `group` | tekst, wym. | Nazwa grupy, wskazywana jako `@grupa`. |
| `comment` | tekst | |

#### `/user/policy`

Co użytkownicy mogą robić w panelu WWW. Menu listowe, nazwa elementu: `name`.

| Parametr | Typ | Opis |
|---|---|---|
| `name` | tekst, wym. | |
| `allow` | lista uprawnień | Przyznane uprawnienia: uprawnienie, `gałąź.*` lub `*`. |
| `deny` | lista uprawnień | Wyjątki od `allow` (wygrywa wpis bardziej szczegółowy). |
| `comment` | tekst | |

Dodatkowy czasownik: `rights <polityka>` pokazuje uprawnienia przyznawane przez politykę.

Uprawnienia:

| Uprawnienie | Znaczenie |
|---|---|
| `live` | Moduł Na żywo: oglądanie kamer. |
| `live.views` | Zapisywanie i usuwanie własnych widoków (układów kafelków). |
| `live.map`, `live.map.edit` | Mapa kamer; umieszczanie i obracanie kamer na mapie. |
| `live.thumbs` | Miniatury wszystkich kamer. |
| `live.ptz`, `live.ptz.edit` | Sterowanie PTZ; zapis zamiany kierunków PTZ kamery. |
| `archive` | Moduł Archiwum: wyszukiwanie nagrań, miniatury, ruch. |
| `archive.export` | Kopiowanie nagrań do magazynów archiwum (zadania). |
| `archive.play` | Odtwarzanie lokalnych nagrań w panelu. |
| `archive.download` | Pobieranie nagranych plików i gotowych zadań. |
| `archive.jobs` | Anulowanie i ponawianie zadań. |
| `system` | Moduł System: przegląd, rejestratory, kamery, dyski, zadania, alerty. |
| `system.logs` | Logi usług kamer. |
| `system.record` | Harmonogramy nagrywania rejestratorów lokalnych. |
| `system.nvr.reboot` / `.backup` / `.firmware` / `.clock` | Restart, kopia konfiguracji, aktualizacja firmware'u, zegar zdalnych rejestratorów. |
| `system.events`, `system.events.ack` | Dziennik zdarzeń; potwierdzanie zdarzeń. |
| `system.events.system` | Dziennik systemowy (logowania, zmiany konfiguracji, usunięcia). |

Uprawnienie podrzędne nie działa bez nadrzędnego: `archive.play` wymaga także `archive` (albo użyj `archive.*`). Symbole wieloznaczne: `live.*`, `live.map.*`, `live.ptz.*`, `archive.*`, `system.*`, `system.nvr.*`, `system.events.*`, `*`.

```
/user/policy add name=viewer allow=live.*,archive,archive.play
/user/policy add name=operator allow=* deny=system.nvr.firmware
```

#### `/user`

Użytkownicy panelu WWW. Menu listowe, nazwa elementu: `user`.

| Parametr | Typ | Opis |
|---|---|---|
| `user` | tekst, wym. | Login. |
| `groups` | lista, ref `/user/group` | Grupy użytkownika. |
| `policy` | ref `/user/policy` | Uprawnienia w panelu WWW; brak: żadnych uprawnień. |
| `password` | tekst, tajny | Zapisywane wyłącznie jako `password-hash`. |
| `password-hash` | tekst, tajny | Skrót crypt: `$6$...` (SHA-512) lub `$5$...` (SHA-256). |
| `disabled` | yes\|no | |
| `comment` | tekst | |

Dodatkowy czasownik: `rights <użytkownik>` pokazuje, co użytkownik może w panelu WWW.

#### `/nvr`

Rejestratory. Menu listowe, nazwa elementu: `name`.

| Parametr | Typ | Opis |
|---|---|---|
| `type` | `local\|dahua\|uniview\|tiandy\|dvrip`, wym. | `local`: ffnvr sam nagrywa do `/storage`; pozostałe: rejestrator sprzętowy czytany przez jego API. |
| `name` | tekst, wym. | |
| `storage` | ref `/storage` | `type=local`: gdzie nagrywa. |
| `retention` | czas | `type=local`: jak długo trzymać nagrania, np. `30d` (puste: zawsze). |
| `record-schedule` | ref `/record/schedule` | `type=local`: harmonogram wszystkich jego kamer (wygrywa harmonogram kamery; puste: nagrywanie ciągłe). |
| `host` | tekst | Zdalny: adres, np. `192.168.1.2`, `192.168.1.2:8080`, `https://nvr.lan` (Tiandy port 3001, DVRIP 34567). |
| `username` | tekst | Zdalny. |
| `password` | tekst, tajny | Zdalny. |
| `auto-add` | yes\|no | Zdalny: nowe kamery na rejestratorze dodaje worker (domyślnie `yes`). |
| `http-port` | liczba | `type=tiandy`: port interfejsu WWW (domyślnie 80). |
| `api` | `rpc\|cgi` | `type=dahua`: JSON-RPC (domyślnie) lub CGI (starsze firmware'y). |
| `speed` | liczba | `type=uniview`: mnożnik prędkości pobierania/odtwarzania (RTSP Speed; odtwarzacz domyślnie 8, `1` = czas rzeczywisty). |
| `fps` | liczba | `type=tiandy`: liczba klatek pobieranych plików (domyślnie: ze strumienia, inaczej 20). |
| `events` | `no\|subscribe` | `subscribe`: serwer alarmów łączy się z rejestratorem po zdarzenia (Uniview: wysyła je do `alarm-lapi`, który trzeba włączyć). Domyślnie `no`. |
| `event-filter` | ref `/event/filter` | Które rodzaje zdarzeń zachować (brak: `default-filter` z `/event`). |
| `event-id` | tekst | Jak rejestrator przedstawia się serwerowi alarmów, gdy nie zdradza tego adres (NAT): numer seryjny (DVRIP) albo login / nadawca SMTP. |
| `valid-users` | lista, ref `/user`, `@/user/group` | Kto może go używać w panelu WWW, np. `maciej,@firma2`. |
| `disabled` | yes\|no | |
| `comment` | tekst | |

Dodatkowe czasowniki:

- `scan <nvr>` – odczytuje kamery i stan zdalnego rejestratora, dodaje brakujące kamery.
- `probe <nvr>` – pokazuje surowe odpowiedzi zdalnego rejestratora (diagnostyka).

`print` dopisuje bieżący stan każdego rejestratora (np. `UNREACHABLE: ...`).

#### `/camera/profile`

Wspólne ustawienia kamer rejestratorów lokalnych (login, schemat adresu). Wartości samej kamery mają pierwszeństwo. Menu listowe, nazwa elementu: `name`.

| Parametr | Typ | Opis |
|---|---|---|
| `name` | tekst, wym. | |
| `username` | tekst | Login kamer. |
| `password` | tekst, tajny | Hasło kamer. |
| `stream` | tekst | Schemat adresu, np. `rtsp://<host>/<ch>/1`. Zmienne: `<host>` `<ch>` `<ch0>` `<user>` `<password>`. |
| `stream-lq` | tekst | Schemat strumienia niskiej jakości (puste: wyznaczany ze `stream`; `none`: brak LQ). |
| `engine` | `internal\|go2rtc` | Jak w `/camera`. |
| `type` | `dahua\|dvrip\|hikvision\|tiandy\|uniview` | Producent kamer. |
| `ptz` | yes\|no | Sterowanie PTZ (domyślnie `no`). |
| `comment` | tekst | |

#### `/camera`

Kamery. Menu listowe, nazwa elementu: `name` (unikalna w obrębie rejestratora), alias: `alias`.

| Parametr | Typ | Opis |
|---|---|---|
| `name` | tekst, wym. | Unikalna w obrębie nvr; używają jej nagrania i usługa. |
| `alias` | tekst | Opcjonalna przyjazna nazwa, unikalna w obrębie nvr (także wśród nazw). |
| `nvr` | ref `/nvr`, wym. | |
| `stream` | tekst | `rtsp://host:port/ścieżka` (wymagany na nvr `type=local`, o ile nie daje go profil). Przy `engine=go2rtc`: dowolne źródło go2rtc, np. `dvrip://user:pass@host?channel=0&subtype=0`. |
| `stream-lq` | tekst | Strumień niskiej jakości (sub) do podglądu. Puste: wyznaczany ze `stream` dla adresów Uniview, Tiandy, Dahua, Hikvision, XMeye i `dvrip://`; `none`: brak LQ. |
| `engine` | `internal\|go2rtc` | `internal` (łączy się ffmpeg, tylko RTSP; domyślnie) lub `go2rtc` (ffmpeg nagrywa lokalną kopię z go2rtc, podgląd współdzieli połączenie). |
| `channel` | liczba | Kanał na zdalnym rejestratorze (tam wymagany); lokalnie: `<ch>` schematu. |
| `profile` | ref `/camera/profile` | Lokalnie: wspólne ustawienia. |
| `host` | tekst | Lokalnie: `<host>` schematu, np. `192.168.1.2` lub `192.168.1.2:8554`. |
| `username` | tekst | Lokalnie: nadpisuje login z profilu. |
| `password` | tekst, tajny | Lokalnie: nadpisuje hasło z profilu (`""` nadpisuje brakiem hasła). |
| `group` | tekst | Dowolny tekst, widoczny w opisie usługi. |
| `enabled` | yes\|no | Domyślnie `yes`. |
| `comment` | tekst | |
| `type` | `dahua\|dvrip\|hikvision\|tiandy\|uniview` | Lokalnie: producent / protokół kamery. Kamera zdalnego rejestratora przyjmuje typ rejestratora. |
| `ptz` | yes\|no | Sterowanie PTZ (lokalnie: przez `type`, domyślnie `no`; zdalnie: przez rejestrator, domyślnie `yes`, gdy potrafi). |
| `ptz-host` | tekst | Lokalnie: adres (i port) dla PTZ i `events=subscribe` (domyślnie: `host`, inaczej host ze strumienia). |
| `ptz-channel` | liczba | Lokalnie: kanał PTZ (domyślnie: `channel`, inaczej 1). |
| `ptz-swap-horizontal` | yes\|no | Zamienione lewo i prawo (kamera odwrócona lub w lustrzanym odbiciu). |
| `ptz-swap-vertical` | yes\|no | Zamienione góra i dół. |
| `events` | `no\|subscribe` | Lokalnie: serwer alarmów łączy się z kamerą po zdarzenia. Tiandy: własny protokół, port 3001; Dahua: eventManager po HTTP; Hikvision: ISAPI alertStream (port 443 = HTTPS); Uniview: LAPI wysyłane do `alarm-lapi`. Domyślnie `no`. |
| `record-schedule` | ref `/record/schedule` | Lokalnie: harmonogram kamery (ważniejszy niż harmonogram rejestratora). |
| `event-filter` | ref `/event/filter` | Lokalnie: które rodzaje zdarzeń zachować, np. `*/ALARM/*` (Tiandy), `VideoMotion,VideoBlind` (Dahua), `VMD,tamperdetection` (Hikvision), `MotionAlarm,MaskImageAlarm` (Uniview). |

`print` dopisuje bieżący stan każdej kamery; `print detail` pokazuje wartości odziedziczone z profilu i wyznaczone automatycznie.

#### `/camera/position`

Położenie kamer na mapie w panelu WWW. Menu listowe (zapisywane także przez panel, gdy użytkownik z `live.map.edit` umieści kamerę).

| Parametr | Typ | Opis |
|---|---|---|
| `nvr` | tekst, wym. | Rejestrator. |
| `camera` | tekst, wym. | Kamera. |
| `lat` | tekst, wym. | Szerokość geograficzna. |
| `long` | tekst, wym. | Długość geograficzna. |
| `direction` | tekst | Kierunek patrzenia w stopniach od północy (0..359). |
| `angle` | tekst | Kąt widzenia w stopniach (domyślnie 90). |
| `range` | tekst | Zasięg w metrach (domyślnie 30). |

#### `/record/day`

Harmonogram nagrywania: jak wygląda jeden dzień. Menu listowe, nazwa elementu: `name`.

| Parametr | Typ | Opis |
|---|---|---|
| `name` | tekst, wym. | |
| `spans` | lista | Przedziały i ich tryb, np. `"08:00-16:00=regular,16:00-22:00=alarm"`; `24:00` kończy dzień; przedziały nie mogą na siebie zachodzić. |
| `default` | `regular\|alarm\|none` | Tryb czasu nieobjętego przedziałami (domyślnie `none`). |
| `comment` | tekst | |

Tryby:

- `regular` – wszystko jest zachowywane.
- `alarm` – zachowywane tylko otoczenie alarmów kamery (pre-alarm przed, post-alarm po). Działa dla kamer, które wysyłają alarmy: `type=dvrip` (przez `alarm-dvrip`) oraz `type=tiandy`, `dahua`, `hikvision`, `uniview` z `events=subscribe`.
- `none` – nic nie jest zachowywane.

Kamery zawsze nagrywają w sposób ciągły; worker następnie zostawia, przycina (bez ponownego kodowania) albo usuwa każdy zakończony 15-minutowy segment zgodnie z harmonogramem.

#### `/record/schedule`

Harmonogram tygodniowy, przypisywany przez `record-schedule=` w `/nvr` (wszystkie jego kamery) lub w `/camera` (ma pierwszeństwo). Menu listowe, nazwa elementu: `name`.

| Parametr | Typ | Opis |
|---|---|---|
| `name` | tekst, wym. | |
| `mon` … `sun` | ref `/record/day` | Szablon dnia; brak: nagrywanie ciągłe. |
| `week` | tekst | Skrót dla `mon=`…`sun=`: `"mon-fri:biuro,sat-sun:weekend"` (zapisywany jako poszczególne dni). |
| `pre-alarm` | czas | Tryb alarm: ile zachować przed alarmem, 0s–5m (domyślnie `10s`). |
| `post-alarm` | czas | Tryb alarm: ile zachować po alarmie, 0s–10m (domyślnie `60s`). |
| `alarm-filters` | lista, ref `/event/filter` | Oprócz detekcji ruchu za alarmy uznawane są rodzaje z tych filtrów (`action=accept`). |
| `alarm-silence` | czas | Kamery wysyłające alarmy (DVRIP): brak jakiegokolwiek zdarzenia przez ten czas oznacza, że alarmy mogą nie docierać – zachowywany jest cały czas i zgłaszany alert (domyślnie `1d`; `0`: nigdy). |
| `comment` | tekst | |

Dodatkowy czasownik: `status <harmonogram>` – gdzie harmonogram jest używany, czy docierają alarmy jego kamer, co zrobił worker.

```
/record/day
add name=biuro default=none spans="08:00-16:00=regular"
add name=noc default=alarm spans="06:00-22:00=regular"
/record/schedule
add name=tydzien week="mon-fri:biuro,sat-sun:noc" pre-alarm=10s post-alarm=60s
/nvr
set nvr01 record-schedule=tydzien
```

#### `/event`

Ustawienia przechowywania zdarzeń (menu pojedyncze: `set k=v`).

| Parametr | Typ | Opis |
|---|---|---|
| `retention` | czas | Jak długo przechowywać zdarzenia, np. `90d` (domyślnie `30d`, co najmniej `1h`). |
| `gap` | czas | Powiadomienia tej samej kamery i rodzaju bliższe niż ten czas tworzą jedno zdarzenie (domyślnie `5s`; `0s`: nigdy nie łączone). |
| `default-filter` | ref `/event/filter` | Filtr rejestratorów bez `event-filter=` i nieznanych nadawców. Brak: odrzucane są `NewFile`, `TimeChange`, `NTPAdjustTime`, `RtspSessionDisconnect`, `SnapManual`. |

#### `/event/filter`

Które rodzaje zdarzeń są zachowywane. Menu listowe, nazwa elementu: `name`.

| Parametr | Typ | Opis |
|---|---|---|
| `name` | tekst, wym. | |
| `action` | `accept\|drop`, wym. | `accept`: zachowywane tylko wymienione rodzaje; `drop`: wymienione są odrzucane, pozostałe zachowywane. |
| `kinds` | lista | Rodzaje zdarzeń w postaci wysyłanej przez rejestratory, np. `VideoMotion,VideoLoss`. `*` oznacza dowolny tekst, zamiast `\` można pisać `/` (np. `*/ALARM/*` dla alarmów Tiandy). Wielkość liter nie ma znaczenia. |
| `comment` | tekst | |

Filtr zastępuje wbudowaną listę, a nie uzupełnia jej.

```
/event/filter
add name=dahua-nvr action=drop kinds=VideoMotion,RtspSessionDisconnect
add name=tiandy-alarmy action=accept kinds=*/ALARM/*
/event
set retention=90d gap=5s default-filter=dahua-nvr
```

#### `/event/log` (menu akcji)

Odebrane zdarzenia i dziennik systemowy. To nie jest konfiguracja – nic nie jest zapisywane.

| Czasownik | Opis |
|---|---|
| `print [filtry]` | Lista zdarzeń (najnowsze najpierw). |
| `ack <numery>` lub `ack [filtry]` | Potwierdzenie zdarzeń. |

| Parametr | Typ | Opis |
|---|---|---|
| `nvr` | tekst | Rejestrator. |
| `camera` | tekst | Kamera (jej nazwa). |
| `kind` | tekst | np. `VideoMotion`, `VideoLoss`, `Mail`, `VMD`, `MotionAlarm`; dziennik systemowy: `Login`, `LoginFailed`, `ConfigSaved`, `RecordingsDeleted`, `NvrOffline`… `*` i `/` jak w filtrach. |
| `origin` | `recorders\|system` | Zdarzenia z urządzeń albo dziennik systemowy. |
| `user` | tekst | Dziennik systemowy: kto to zrobił. |
| `text` | tekst | Zawarty w szczegółach lub adresie nadawcy. |
| `from` / `to` | `"RRRR-MM-DD GG:MM[:SS]"` | Zakres czasu. |
| `unacked` | yes\|no | `print`: tylko niepotwierdzone. |
| `count` | liczba | `print`: najwyżej tyle najnowszych (domyślnie 50). |
| `raw` | yes\|no | `print`: pod każdym zdarzeniem pierwsze surowe powiadomienie. |

```
/event/log print nvr=nvr02 kind=VideoMotion
/event/log print origin=system count=20
/event/log ack 12,13
/event/log ack nvr=nvr02
```

#### `/nvr/download` (menu akcji)

Archiwizacja nagrań do plików `.mp4` jako zadania wykonywane w tle przez workera.

| Czasownik | Opis |
|---|---|
| `add ...` | Tworzy zadanie. |
| `print` | Pokazuje zadania. |
| `cancel <zadanie>` | Zatrzymuje zadanie. |
| `remove <zadanie>` | Usuwa zadanie. |

| Parametr | Typ | Opis |
|---|---|---|
| `camera` | tekst, wym. | Nazwa lub alias kamery. |
| `nvr` | tekst | Potrzebny, gdy nazwa kamery występuje na kilku rejestratorach. |
| `storage` | ref `/storage`, wym. | Dokąd trafia archiwum. |
| `path` | tekst | Podkatalog magazynu, np. `sprawa-12/wejscie`. |
| `layout` | `nvr-camera\|flat` | `nvr-camera` (domyślnie): pliki w `<path>/<nvr>/<kamera>/`; `flat`: wszystkie w `<path>/`. |
| `file` | tekst | Nazwa pliku (domyślnie `<nvr>_<kamera>_<start>_<koniec>.mp4`; przy `block`: przedrostek). |
| `block` | czas | Podział na pliki tej długości: `15m`, `1h`, `1h30m`, `1d` (domyślnie: jeden plik). |
| `block-align` | `clock\|start` | `clock` (domyślnie): 11:50–12:00, 12:00–13:00…; `start`: 11:50–12:50, 12:50–13:50… |
| `start` | `"RRRR-MM-DD GG:MM[:SS]"`, wym. | Czas lokalny. |
| `end` | `"RRRR-MM-DD GG:MM[:SS]"`, wym. | Czas lokalny. |
| `comment` | tekst | |

```
/nvr/download add camera=front storage=disk1 path=sprawa-12 \
    start="2026-11-07 08:00" end="2026-11-07 12:00" block=1h
/nvr/download print
```

Zadania wykonuje worker (`ffnvr-worker.service`), więc konfiguracja musi być wcześniej zatwierdzona przez `commit`/`apply`.

#### `/nvr/tools` (menu akcji)

Akcje na zdalnych rejestratorach.

| Czasownik | Opis |
|---|---|
| `reboot nvr=<nvr>` | Restart rejestratora. |
| `backup nvr=<nvr> storage=<magazyn> [directory=...]` | Zapis kopii konfiguracji rejestratora (domyślny katalog `backup`). |
| `upgrade nvr=<nvr> file=<ścieżka>` | Aktualizacja firmware'u z pliku `.bin` na tym serwerze (Dahua). |
| `datetime nvr=<nvr> [...]` | Odczyt lub ustawienie zegara i NTP. |

| Parametr | Typ | Opis |
|---|---|---|
| `nvr` | ref `/nvr` | Rejestrator. |
| `storage` | ref `/storage` | backup: gdzie trafia plik. |
| `directory` | tekst | backup: podkatalog magazynu, np. `backup/nvr02`. |
| `file` | tekst | upgrade: plik firmware'u. |
| `time` | tekst | datetime: `now` (czas tego serwera) lub `RRRR-MM-DD GG:MM:SS`. |
| `ntp` | yes\|no | datetime: synchronizacja z NTP. |
| `ntp-server` | tekst | datetime: adres serwera NTP. |
| `ntp-port` | tekst | datetime: port serwera NTP (domyślnie: bez zmian). |
| `ntp-interval` | tekst | datetime: minuty między synchronizacjami (domyślnie: bez zmian). |

```
/nvr/tools/reboot nvr=nvr02
/nvr/tools/backup nvr=nvr02 storage=disk1 directory=backup/nvr02
/nvr/tools/upgrade nvr=nvr02 file=/tmp/DH_NVR_V4.005.bin
/nvr/tools/datetime nvr=nvr02
/nvr/tools/datetime nvr=nvr02 time=now
/nvr/tools/datetime nvr=nvr02 ntp=yes ntp-server=pool.ntp.org ntp-interval=60
```

#### `/ip/service`

Usługi sieciowe. Elementy stałe: nie można ich dodawać ani usuwać, tylko `set`; `unset` przywraca wartość domyślną.

| Element | Domyślny port | Opis |
|---|---|---|
| `www` | 80 | Panel WWW. |
| `alarm-dvrip` | 15002 | Serwer alarmów XMeye/DVRIP. |
| `alarm-smtp` | 2525 | Serwer SMTP dla e-maili z rejestratorów i kamer. |
| `alarm-lapi` | 8091 | Odbiornik zdarzeń Uniview LAPI (`events=subscribe` kamer Uniview). |

| Parametr | Typ | Opis |
|---|---|---|
| `name` | tekst | Tylko do odczytu. |
| `port` | liczba | |
| `address` | lista | Kto może się łączyć: adresy lub sieci, np. `192.168.0.0/16,10.1.2.3` (puste: wszyscy). |
| `proxy` | lista | Reverse proxy przed panelem, którym wierzy się w `X-Forwarded-For`. |
| `enabled` | yes\|no | Domyślnie wszystkie wyłączone. |
| `comment` | tekst | |

```
/ip/service set www enabled=yes port=8080 address=192.168.0.0/16
/ip/service set alarm-dvrip enabled=yes
```

#### `/system/settings`

| Parametr | Typ | Opis |
|---|---|---|
| `poll-interval` | czas | Jak często odczytywać zdalne rejestratory (firmware, dyski, kamery); domyślnie `5m`, co najmniej `1m`. |
| `disk-reserve` | rozmiar | Wolne miejsce utrzymywane na każdym magazynie rejestratorów lokalnych, np. `2G`: gdy go brakuje, usuwane są najstarsze nagrania niezależnie od retencji (domyślnie: brak). |

#### `/system/systemd`

Izolacja usług nagrywających (ffmpeg kamer). `print detail` pokazuje, co dostają unity.

| Parametr | Typ | Opis |
|---|---|---|
| `security` | `none\|basic\|high\|custom` | `none` (domyślnie: root, bez piaskownicy); `basic` (root w piaskownicy: system tylko do odczytu, zapis tylko do katalogu kamery); `high` (basic + własne konto, bez urządzeń, zablokowane jądro i wywołania systemowe); `custom` (basic zmieniony polami poniżej). |
| `memory-max` | rozmiar | Limit pamięci jednego nagrywania, np. `200M`, `1G`, `unlimited` (domyślnie `200M`). |
| `extra` | lista | Dodatkowe dyrektywy `[Service]`, np. `"CPUQuota=50%,IOWeight=50"` (tylko zasoby i izolacja). |
| `user` | tekst | `high`: konto (domyślnie `ffnvr`); `custom`: domyślnie `root`. |
| `no-new-privileges` | yes\|no | custom: `NoNewPrivileges` (domyślnie yes). |
| `protect-system` | `no\|yes\|full\|strict` | custom: `ProtectSystem` (domyślnie strict). |
| `protect-home` | `no\|yes\|read-only\|tmpfs` | custom: `ProtectHome` (domyślnie yes). |
| `private-tmp` | yes\|no | custom: `PrivateTmp` (domyślnie yes). |
| `private-devices` | yes\|no | custom: `PrivateDevices` (domyślnie no). |
| `protect-kernel` | yes\|no | custom: parametry/moduły/logi jądra, cgroups, zegar, nazwa hosta (domyślnie no). |
| `restrict` | yes\|no | custom: przestrzenie nazw, realtime, SUID/SGID, personality, rodziny adresów (domyślnie no). |
| `system-call-filter` | yes\|no | custom: `SystemCallFilter=@system-service`, `MemoryDenyWriteExecute` (domyślnie no). |
| `drop-capabilities` | yes\|no | custom: puste `CapabilityBoundingSet` (domyślnie no). |

Dla `security=high` konto musi istnieć (`make install` je tworzy):

```bash
useradd --system --no-create-home --shell /usr/sbin/nologin ffnvr
```

#### `/system/motion-detector`

Jak powstają paski ruchu w archiwum na podstawie lokalnych nagrań.

| Parametr | Typ | Opis |
|---|---|---|
| `mode` | `auto\|heuristic\|manual` | `auto`: pakiety, albo klatki kluczowe, gdy pakiety nie odzwierciedlają ruchu; `heuristic`: ten sam wybór spośród `methods`; `manual`: jedna metoda z `methods` (domyślnie `auto`). |
| `methods` | lista z `packets\|keyframes\|pixels` | Metody dla `heuristic` / `manual`. |
| `gap` | czas | Przerwa kończąca przedział (domyślnie `3s`). |
| `min-duration` | czas | Najkrótsze zdarzenie (domyślnie `1s`). |
| `padding` | czas | Doliczane przed i po każdym przedziale (domyślnie `2s`). |
| `skip` | czas | Pomijany początek każdego segmentu (domyślnie `4s`; `0s`: bez pomijania). |
| `window` | czas | packets: okno mediany ruchomej tła, w każdą stronę (domyślnie `60s`). |
| `ratio-min` | liczba | packets: najmniejszy sygnał (× tło), który się liczy (domyślnie 1.12). |
| `noise-k` | liczba | packets: próg = 1 + noise-k × szum (domyślnie 4). |
| `noise-k-low` | liczba | packets: niższy próg histerezy (domyślnie 2). |
| `noise-max` | liczba | packets: powyżej tego szumu rozmiarom pakietów się nie ufa (domyślnie 0.30). |
| `cbr-flat` | liczba | packets: stosunek szumu, powyżej którego strumień uznaje się za CBR (domyślnie 6). |
| `max-verify` | liczba | packets: ile przedziałów najwyżej sprawdzać w obrazie (domyślnie 30). |
| `pixel-diff` | 0–255 | pixels, keyframes: zmiana jasności piksela uznawana za ruch (domyślnie 20). |
| `pixel-min-percent` | % | pixels, keyframes: jaka część obrazu musi się zmienić (domyślnie 0.10). |
| `pixel-fps` | liczba | pixels: analizowane klatki na sekundę (domyślnie 4). |
| `pixel-width` | px | pixels, keyframes: szerokość, do której skalowany jest obraz (domyślnie 320). |
| `osd-height` | px | pixels, keyframes: pomijany pas u góry (zegar), po skalowaniu (domyślnie 24). |
| `global-percent` | % | pixels, keyframes: większa zmiana naraz to przełączenie światła/IR, nie ruch (domyślnie 50). |
| `keyframe-max-gop` | czas | Najdłuższy GOP dla metody keyframes (domyślnie `5s`). |

#### `/system/live`

Podgląd na żywo przez go2rtc na localhost, udostępniany przez panel WWW.

| Parametr | Typ | Opis |
|---|---|---|
| `enabled` | yes\|no | Uruchamia go2rtc (domyślnie `no`). |
| `go2rtc` | tekst | Program go2rtc (domyślnie `/usr/local/bin/go2rtc`). |
| `port` | liczba | Port API na 127.0.0.1 (domyślnie 1984). |
| `rtsp-port` | liczba | Port RTSP na 127.0.0.1, dla kamer z `engine=go2rtc` (domyślnie 8554). |
| `map-tiles` | tekst | Adres kafelków mapy (domyślnie `https://tile.openstreetmap.org/{z}/{x}/{y}.png`). |
| `transcode` | yes\|no | H.264 dla przeglądarek bez H.265, transkodowane na żądanie (domyślnie `no`). |
| `transcode-users` | lista, ref `/user`, `@/user/group` | Kto może z niego korzystać; puste: nikt. |
| `transcode-height` | liczba | Wysokość transkodowanego HQ, 144..2160 (domyślnie 720). |
| `transcode-max` | liczba | Maks. liczba jednocześnie transkodowanych strumieni HQ, 0..32 (domyślnie 2). |
| `transcode-lq-height` | liczba | Wysokość transkodowanego LQ, 144..2160, lub 0: rozmiar oryginalny (domyślnie). |
| `transcode-lq-max` | liczba | Maks. liczba jednocześnie transkodowanych strumieni LQ, 0..32 (domyślnie 4). |
| `transcode-hardware` | `none\|auto\|vaapi\|cuda\|v4l2m2m\|rkmpp` | Koder: `none` (libx264, domyślnie), `auto`, VAAPI (Intel/AMD), CUDA (NVIDIA), V4L2 M2M, Rockchip MPP. |

#### `/system/snapshot`

Miniatury wszystkich kamer do podglądu, wykonywane przez workera przez go2rtc (wymaga `/system/live enabled=yes`).

| Parametr | Typ | Opis |
|---|---|---|
| `enabled` | yes\|no | Wykonywanie miniatur (domyślnie `no`). |
| `interval` | `daily\|weekly` | Domyślnie `daily`. |
| `time` | `GG:MM` | Czas lokalny (domyślnie `03:00`). |
| `day` | `monday`…`sunday` | `interval=weekly`: dzień tygodnia (domyślnie `monday`). |
| `quality` | `lq\|hq` | Strumień źródłowy (domyślnie `lq`). |
| `attempts` | 1–10 | Liczba prób na kamerę (domyślnie 3). |

#### `/system/www`

Wygląd panelu WWW.

| Parametr | Typ | Opis |
|---|---|---|
| `name` | tekst | Nazwa wyświetlana zamiast „ffnvr”. |
| `tagline` | tekst | Linia pod nazwą na stronie logowania. |
| `login-name` | yes\|no | Nazwa na stronie logowania (domyślnie yes). |
| `login-logo` | yes\|no | Logo na stronie logowania (domyślnie yes). |

---

## Pliki i katalogi

| Ścieżka | Zawartość |
|---|---|
| `/usr/local/bin/ffnvr` | Program (unity wywołują go z tej ścieżki). |
| `/etc/ffnvr/config.rsc` | Domyślna konfiguracja. |
| `/etc/ffnvr/go2rtc.yaml` | Konfiguracja go2rtc generowana przez ffnvr. |
| `/etc/systemd/system/ffnvr-*.service` | Wygenerowane unity. |
| `/var/lib/ffnvr/jobs` | Zadania archiwizacji. |
| `/var/lib/ffnvr/events` | Baza zdarzeń (SQLite) i zdjęcia. |
| `/var/lib/ffnvr/status` | Ostatni znany stan zdalnych rejestratorów. |
| `/var/lib/ffnvr/profile` | Profile i sesje użytkowników panelu WWW. |
| `<storage>/<nvr>/<kamera>/` | Nagrania: `RRRR-MM-DDTGG-MM-SS.mkv`, segmenty 15-minutowe. |
| `<storage>/<nvr>/.<kamera>/` | Dane techniczne kamery: indeks, podsumowanie, stan harmonogramu. |
| `~/.ffnvr_history` | Historia powłoki. |

## Rozwój

```bash
make test     # go vet ./... && go test ./...
make build
```

Flagi `-root` i `-no-services` pozwalają sprawdzać konfiguracje na katalogu testowym bez dotykania systemu:

```bash
mkdir -p /tmp/ffnvr-test
ffnvr diff  -root /tmp/ffnvr-test -c ./przyklad.rsc
ffnvr apply -root /tmp/ffnvr-test -c ./przyklad.rsc -no-services
```
