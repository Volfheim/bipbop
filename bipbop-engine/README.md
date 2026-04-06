# lionheart

SOCKS5-прокси и VPN, маскирующий трафик под WebRTC-сессии видеоплатформы WB Stream.

<p align="center">
  <a href="#russian">🇷🇺 Русский</a> •
  <a href="#english">🇺🇸 English</a>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/Go-1.22+-blue?logo=go">
  <img src="https://img.shields.io/badge/Android-Kotlin%20%2B%20Compose-green?logo=android">
  <img src="https://img.shields.io/badge/License-GPL--3.0-green">
  <img src="https://img.shields.io/badge/version-1.2-orange">
</p>

---

<a id="russian"></a>

## 🇷🇺 Русский

### Дисклеймер

Проект создан для изучения протоколов WebRTC, KCP и архитектуры TURN-серверов. Автор настоятельно не рекомендует использовать его для обхода сетевых ограничений, DPI или корпоративных файрволов. То, что программа справляется с этим на скорости гигабитного канала — непредвиденное последствие выбранного стека технологий.

### Что это

Lionheart — клиент-серверный туннель с двумя клиентами: CLI (все платформы) и Android-приложение (Kotlin + Jetpack Compose). Клиент открывает локальный SOCKS5-порт или полноценный VPN-интерфейс, весь трафик через них пакуется в KCP (reliable UDP), шифруется AES-256, и передаётся на ваш VPS через TURN-реле WB Stream. Для внешнего наблюдателя это выглядит как обычный WebRTC-трафик к серверам Wildberries.

### Что нового в v1.2

- **Android-приложение** — полноценный VPN-клиент с Material 3 интерфейсом
- **Автоустановка сервера** — приложение само ставит Lionheart на VPS по SSH
- **Обновление сервера из приложения** — одна кнопка для обновления удалённого бинарника
- **Split tunneling** — выбор приложений, которые идут через VPN (или мимо)
- **Kill Switch** — блокировка интернета при обрыве VPN
- **Блокировка рекламы** — AdGuard DNS на уровне VPN
- **Маскировка приложения** — смена иконки и названия (Калькулятор, Часы, Заметки и т.д.)
- **QR-коды** — сканирование и генерация смарт-ключей
- **4 языка** — английский, русский, белорусский, татарский
- **Проверка версии сервера** — блокировка подключения к устаревшему серверу
- **Поддержка GrapheneOS** — корректная работа с Kill Switch и Always-On VPN
- **Множественные серверы** — управление несколькими VPS из одного приложения
- **Выбор DNS, MTU, IP-протокола** — настройка на уровне каждого сервера
- **Quick Settings Tile** — быстрое подключение из шторки Android

### Архитектура

```
┌──────────────────┐     ┌──────────────────┐
│  Android-клиент  │     │   CLI-клиент      │
│  Kotlin/Compose  │     │   Go binary       │
│  + tun2socks     │     │   SOCKS5 :1080    │
└────────┬─────────┘     └────────┬──────────┘
         │                        │
         └──────────┬─────────────┘
                    │
         ┌──────────▼──────────┐
         │      core/ (Go)     │
         │  Tunnel, session,   │
         │  reconnect, creds   │
         └──────────┬──────────┘
                    │
         ┌──────────▼──────────┐
         │  KCP/UDP + AES-256  │
         │  + yamux mux        │
         └──────────┬──────────┘
                    │
         ┌──────────▼──────────┐
         │  WB Stream TURN     │
         │  (WebRTC relay)     │
         └──────────┬──────────┘
                    │
         ┌──────────▼──────────┐
         │   VPS-сервер (Go)   │
         │  KCP + SOCKS5       │
         └──────────┬──────────┘
                    │
              ┌─────▼─────┐
              │ Интернет   │
              └────────────┘
```

### Как это работает

Клиент и сервер не общаются напрямую. Между ними стоит TURN-сервер платформы WB Stream, который ретранслирует UDP-пакеты в обе стороны.

**Получение TURN-кредов (v1.1+ — без браузера):**

Клиент повторяет флоу веб-клиента WB Stream через обычные HTTP-запросы:

1. `POST /auth/api/v1/auth/user/guest-register` — регистрация гостя, получение JWT
2. `POST /api-room/api/v2/room` — создание видеокомнаты
3. `POST /api-room/api/v1/room/{id}/join` — вход в комнату
4. `GET /api-room-manager/api/v1/room/{id}/token` — получение LiveKit JWT

Затем клиент открывает WebSocket к LiveKit-серверу (`wss://wbstream01-el.wb.ru:7880/rtc`). LiveKit отвечает protobuf `SignalResponse` → `JoinResponse` с массивом ICE-серверов (TURN-адреса, логины, пароли). Минимальный парсер protobuf wire format извлекает их без кодогенерации. WB Stream хранит ICE-серверы в field 5 JoinResponse (стандартный LiveKit — field 9), парсер проверяет оба.

**Установка туннеля:**

С TURN-кредами клиент через pion/turn авторизуется и вызывает `Allocate` для relay-адреса. Через relay устанавливается KCP-соединение с VPS. Ключ шифрования — SHA-256 от пароля из смарт-ключа. Поверх KCP работает yamux-мультиплексор для сотен TCP-потоков через один UDP-канал. На сервере каждый yamux-стрим передаётся SOCKS5-серверу, который выпускает трафик в интернет.

**Android VPN-режим:**

Go-библиотека `golib/` компилируется через `gomobile bind` в .aar. Внутри — tun2socks (gVisor-based) перенаправляет весь трафик с TUN-интерфейса Android в локальный SOCKS5, который идёт через зашифрованный туннель. Поддерживается UDP relay (DNS-over-TCP через SOCKS5 для UDP-ассоциации).

**Надёжность:**

Yamux-сессия пингуется каждые 15 секунд. При обрыве — exponential backoff (2с → 60с). TURN-креды кешируются на 5 минут; после 3 неудач кеш сбрасывается. При запуске убиваются предыдущие экземпляры, systemd unit-файл обновляется автоматически.

### Структура проекта

```
lionheart/
├── config/                     ← ВСЕ НАСТРОЙКИ
│   ├── app.json                ← версия, цвета, порт, языки
│   ├── translations/{en,ru,be,tt}
│   └── branding/countries.json
│
├── core/                       ← ЯДРО ТУННЕЛЯ (общий код)
│   ├── tunnel.go               ← сессия, реконнект, health
│   ├── wb.go                   ← получение TURN-кредов
│   └── protobuf.go             ← парсер protobuf wire format
│
├── cmd/lionheart/              ← CLI (сервер + клиент)
│
├── mobile/
│   ├── golib/                  ← Go → Android мост (gomobile)
│   ├── anet-stub/              ← заглушка для gomobile
│   └── android/                ← Android-приложение
│       └── app/src/main/java/com/lionheart/vpn/
│           ├── ui/             ← Jetpack Compose экраны
│           ├── viewmodel/      ← VpnViewModel
│           ├── service/        ← VpnService, TileService
│           └── data/           ← ServerProfile, PrefsRepository
│
├── build.sh                    ← Главный скрипт сборки
└── output/                     ← Все артефакты
```

### Сборка

Go 1.22+. Chrome не нужен.

**CLI (все платформы):**
```bash
./build.sh cli
```

**Android APK:**
```bash
./build.sh apk          # debug + release
./build.sh debugapk     # только debug + adb install
```

**Всё сразу:**
```bash
./build.sh all
```

Артефакты появляются в `output/` с понятными именами:
```
lionheart-1.2-linux-x64
lionheart-1.2-macos-arm64
lionheart-1.2-windows-x64.exe
lionheart-1.2-android.apk
lionheart-1.2-android-debug.apk
```

### Использование

При первом запуске CLI откроется мастер, который создаст `config.json`.

**Сервер (VPS):**

```bash
./lionheart
# выбрать "1", скопировать смарт-ключ
# опционально установить как systemd-сервис
```

Смарт-ключ — это base64 от `IP:порт|пароль`. Его нужно передать клиенту.

**CLI-клиент:**

```bash
./lionheart
# выбрать "2", вставить смарт-ключ
```

После подключения: `127.0.0.1:1080` (SOCKS5) для локальной машины, `<LAN_IP>:1080` для устройств в той же сети.

**Android-клиент:**

1. Установить APK
2. «Добавить сервер» → «Автоматическая установка» (ввести IP, SSH-пароль) или вставить/сканировать смарт-ключ
3. Нажать кнопку подключения

### Настройка

**Иконка приложения** — `config/app.json`:
```json
"icon": {
    "background_color": "#103bb0",
    "foreground_color": "#ffffff"
}
```

**Добавить язык:**
1. Скопировать `config/translations/en/strings.xml` → `config/translations/uk/strings.xml`
2. Перевести строки
3. Добавить `"uk"` в `app.json` → `supported_languages`
4. `./build.sh apk`

### Зависимости

```
github.com/armon/go-socks5     — SOCKS5-сервер
github.com/gorilla/websocket   — WebSocket-клиент для LiveKit
github.com/hashicorp/yamux     — мультиплексор потоков
github.com/pion/turn/v4        — TURN-клиент
github.com/xtaci/kcp-go/v5     — KCP (reliable UDP)
github.com/xjasonlyu/tun2socks — TUN → SOCKS5 (Android VPN)
golang.org/x/mobile            — gomobile bind
```

### Благодарности

Проект вдохновлён идеями из [vk-turn-proxy](https://github.com/nickolaylavrinenko/vk-turn-proxy).

### Лицензия

GPL-3.0. Pull requests приветствуются.

---

<a id="english"></a>

## 🇺🇸 English

### Disclaimer

This project exists for studying WebRTC, KCP, and TURN server internals. The author strongly advises against using it to bypass network restrictions, DPI, or corporate firewalls. The fact that it does this effortlessly over gigabit-class streaming infrastructure is an unintended architectural side effect.

### What is this

Lionheart is a client-server tunnel with two clients: a CLI (all platforms) and an Android app (Kotlin + Jetpack Compose). The client opens a local SOCKS5 port or a full VPN interface, packs all traffic into KCP (reliable UDP), encrypts it with AES-256, and routes it to your VPS through WB Stream's TURN relay. To any observer it looks like regular WebRTC traffic to Wildberries servers.

### What's new in v1.2

- **Android app** — full VPN client with Material 3 UI
- **Auto server setup** — the app installs Lionheart on your VPS via SSH
- **Remote server update** — one-tap update of the remote binary
- **Split tunneling** — choose which apps go through VPN (or bypass it)
- **Kill Switch** — block internet when VPN drops
- **Ad blocking** — AdGuard DNS at the VPN level
- **App disguise** — change icon and name to Calculator, Clock, Notes, etc.
- **QR codes** — scan and generate smart keys
- **4 languages** — English, Russian, Belarusian, Tatar
- **Server version check** — blocks connection to outdated servers
- **GrapheneOS support** — proper handling of Kill Switch and Always-On VPN
- **Multiple servers** — manage several VPS from one app
- **DNS, MTU, IP protocol** — per-server network configuration
- **Quick Settings Tile** — toggle VPN from Android notification shade

### Architecture

```
┌──────────────────┐     ┌──────────────────┐
│  Android client  │     │   CLI client      │
│  Kotlin/Compose  │     │   Go binary       │
│  + tun2socks     │     │   SOCKS5 :1080    │
└────────┬─────────┘     └────────┬──────────┘
         │                        │
         └──────────┬─────────────┘
                    │
         ┌──────────▼──────────┐
         │      core/ (Go)     │
         │  Tunnel, session,   │
         │  reconnect, creds   │
         └──────────┬──────────┘
                    │
         ┌──────────▼──────────┐
         │  KCP/UDP + AES-256  │
         │  + yamux mux        │
         └──────────┬──────────┘
                    │
         ┌──────────▼──────────┐
         │  WB Stream TURN     │
         │  (WebRTC relay)     │
         └──────────┬──────────┘
                    │
         ┌──────────▼──────────┐
         │   VPS server (Go)   │
         │  KCP + SOCKS5       │
         └──────────┬──────────┘
                    │
              ┌─────▼─────┐
              │  Internet  │
              └────────────┘
```

### How it works

Client and server never talk directly. WB Stream's TURN server sits between them, relaying UDP packets both ways.

**Obtaining TURN credentials (v1.1+ — no browser):**

The client replays the WB Stream web client flow via plain HTTP:

1. `POST /auth/api/v1/auth/user/guest-register` — register guest, get JWT
2. `POST /api-room/api/v2/room` — create a video room
3. `POST /api-room/api/v1/room/{id}/join` — join the room
4. `GET /api-room-manager/api/v1/room/{id}/token` — get a LiveKit JWT

Then the client opens a WebSocket to WB Stream's LiveKit server (`wss://wbstream01-el.wb.ru:7880/rtc`). LiveKit responds with a protobuf `SignalResponse` → `JoinResponse` containing ICE servers (TURN addresses, usernames, credentials). A minimal protobuf wire-format parser extracts them without codegen. WB Stream stores ICE servers in field 5 of JoinResponse (standard LiveKit uses field 9) — the parser checks both.

**Tunnel establishment:**

With TURN credentials, the client authenticates via pion/turn and calls `Allocate` to get a relay address. A KCP connection is established to the VPS through this relay. Encryption key is SHA-256 of the smart-key password. Yamux runs on top of KCP, multiplexing hundreds of TCP streams over a single UDP channel. On the server side, each yamux stream is handed to SOCKS5, which forwards traffic to the internet.

**Android VPN mode:**

The Go library `golib/` is compiled via `gomobile bind` into an .aar. Inside, tun2socks (gVisor-based) redirects all traffic from Android's TUN interface into a local SOCKS5 proxy, which goes through the encrypted tunnel. UDP relay is supported via DNS-over-TCP through SOCKS5 UDP association.

**Reliability:**

The yamux session is pinged every 15 seconds. On disconnect — exponential backoff (2s → 60s cap). TURN credentials are cached for 5 minutes; after 3 failures the cache is flushed. On startup, previous instances are killed and the systemd unit file is auto-updated.

### Project structure

```
lionheart/
├── config/                     ← ALL SETTINGS
│   ├── app.json                ← version, colors, port, languages
│   ├── translations/{en,ru,be,tt}
│   └── branding/countries.json
│
├── core/                       ← TUNNEL CORE (shared code)
│   ├── tunnel.go               ← session, reconnect, health
│   ├── wb.go                   ← TURN credential fetching
│   └── protobuf.go             ← protobuf wire format parser
│
├── cmd/lionheart/              ← CLI (server + client)
│
├── mobile/
│   ├── golib/                  ← Go → Android bridge (gomobile)
│   ├── anet-stub/              ← stub for gomobile
│   └── android/                ← Android app
│       └── app/src/main/java/com/lionheart/vpn/
│           ├── ui/             ← Jetpack Compose screens
│           ├── viewmodel/      ← VpnViewModel
│           ├── service/        ← VpnService, TileService
│           └── data/           ← ServerProfile, PrefsRepository
│
├── build.sh                    ← Main build script
└── output/                     ← All artifacts
```

### Building

Go 1.22+. No Chrome needed.

**CLI (all platforms):**
```bash
./build.sh cli
```

**Android APK:**
```bash
./build.sh apk          # debug + release
./build.sh debugapk     # debug only + adb install
```

**Everything:**
```bash
./build.sh all
```

Artifacts appear in `output/` with clear names:
```
lionheart-1.2-linux-x64
lionheart-1.2-macos-arm64
lionheart-1.2-windows-x64.exe
lionheart-1.2-android.apk
lionheart-1.2-android-debug.apk
```

### Usage

First CLI run opens a setup wizard that creates `config.json`.

**Server (VPS):**

```bash
./lionheart
# pick "1", copy the smart-key
# optionally install as a systemd service
```

The smart-key is base64 of `IP:port|password`. Hand it to the client.

**CLI client:**

```bash
./lionheart
# pick "2", paste the smart-key
```

Once connected: `127.0.0.1:1080` (SOCKS5) for the local machine, `<LAN_IP>:1080` for devices on the same network.

**Android client:**

1. Install the APK
2. "Add server" → "Automatic setup" (enter IP, SSH password) or paste/scan a smart key
3. Tap the connect button

### Configuration

**App icon** — `config/app.json`:
```json
"icon": {
    "background_color": "#103bb0",
    "foreground_color": "#ffffff"
}
```

**Add a language:**
1. Copy `config/translations/en/strings.xml` → `config/translations/uk/strings.xml`
2. Translate the strings
3. Add `"uk"` to `app.json` → `supported_languages`
4. `./build.sh apk`

### Dependencies

```
github.com/armon/go-socks5     — SOCKS5 server
github.com/gorilla/websocket   — WebSocket client for LiveKit
github.com/hashicorp/yamux     — stream multiplexer
github.com/pion/turn/v4        — TURN client
github.com/xtaci/kcp-go/v5     — KCP (reliable UDP)
github.com/xjasonlyu/tun2socks — TUN → SOCKS5 (Android VPN)
golang.org/x/mobile            — gomobile bind
```

### Acknowledgements

Inspired by [vk-turn-proxy](https://github.com/nickolaylavrinenko/vk-turn-proxy).

### License

GPL-3.0. PRs welcome.