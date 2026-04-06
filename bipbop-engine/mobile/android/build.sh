set -e
ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT"
read_config() { python3 -c "import json; print(json.load(open('config/app.json'))$1)" 2>/dev/null || echo "$2"; }
VERSION=$(read_config "['version']" "1.2")
ICON_BG=$(read_config "['icon']['background_color']" "#6E1319")
ICON_FG=$(read_config "['icon']['foreground_color']" "#EDB953")
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
CYAN='\033[0;36m'; BOLD='\033[1m'; NC='\033[0m'
echo -e "${YELLOW}"
echo "  ╔═════════════════════════════════════╗"
echo "  ║  Lionheart v${VERSION} — сборка      ║"
echo "  ╚═════════════════════════════════════╝"
echo -e "${NC}"
MODE="${1:-all}"
OUT="$ROOT/output"
mkdir -p "$OUT"
human_name() {
    local os="$1" arch="$2"
    case "$os" in
        linux)   os_name="linux" ;;
        darwin)  os_name="macos" ;;
        windows) os_name="windows" ;;
        android) os_name="android" ;;
        freebsd) os_name="freebsd" ;;
        *)       os_name="$os" ;;
    esac
    case "$arch" in
        amd64) arch_name="x64" ;;
        arm64) arch_name="arm64" ;;
        386)   arch_name="x86" ;;
        *)     arch_name="$arch" ;;
    esac
    echo "lionheart-${VERSION}-${os_name}-${arch_name}"
}
check_go() {
    command -v go >/dev/null 2>&1 || { echo -e "${RED}❌ Go не найден. https://go.dev/dl/${NC}"; exit 1; }
    echo -e "  Go:  $(go version | awk '{print $3}')"
}
find_android_sdk() {
    ANDROID_HOME="${ANDROID_HOME:-$HOME/Android}"
    for p in "$HOME/Android/Sdk" "$HOME/Android" "$HOME/Library/Android/sdk"; do
        [ -d "$p" ] && { ANDROID_HOME="$p"; break; }
    done
    export ANDROID_HOME
    export PATH="$PATH:$ANDROID_HOME/cmdline-tools/latest/bin:$ANDROID_HOME/platform-tools:$(go env GOPATH)/bin"
    echo -e "  SDK: $ANDROID_HOME"
}
find_ndk() {
    if [ -z "$ANDROID_NDK_HOME" ]; then
        if [ -d "$ANDROID_HOME/ndk" ]; then
            ANDROID_NDK_HOME=$(ls -d "$ANDROID_HOME/ndk"/*/ 2>/dev/null | sort -V | tail -1 | sed 's/\/$//')
        fi
        if [ -z "$ANDROID_NDK_HOME" ] && [ -d "$ANDROID_HOME/ndk-bundle" ]; then
            ANDROID_NDK_HOME="$ANDROID_HOME/ndk-bundle"
        fi
    fi
    export ANDROID_NDK_HOME
    if [ -z "$ANDROID_NDK_HOME" ] || [ ! -d "$ANDROID_NDK_HOME" ]; then
        echo -e "  ${YELLOW}⚠ NDK не найден!${NC}"
        echo -e "  ${YELLOW}  Установи: sdkmanager --install 'ndk;25.2.9519653'${NC}"
        echo -e "  ${YELLOW}  Или: export ANDROID_NDK_HOME=/путь/к/ndk${NC}"
        return 1
    fi
    echo -e "  NDK: $ANDROID_NDK_HOME"
    return 0
}
sync_config() {
    echo -e "${CYAN}→ Синхронизация config → Android...${NC}"
    local ANDROID="$ROOT/mobile/android"
    local RES="$ANDROID/app/src/main/res"
    if [ -f "config/translations/en/strings.xml" ]; then
        mkdir -p "$RES/values"
        cp "config/translations/en/strings.xml" "$RES/values/strings.xml"
    fi
    for LANG_DIR in config/translations/*/; do
        LANG=$(basename "$LANG_DIR")
        [ "$LANG" = "en" ] && continue
        if [ -f "$LANG_DIR/strings.xml" ]; then
            mkdir -p "$RES/values-$LANG"
            cp "$LANG_DIR/strings.xml" "$RES/values-$LANG/strings.xml"
        fi
    done
    cat > "$RES/values/colors.xml" << COLEOF
<?xml version="1.0" encoding="utf-8"?>
<resources>
    <color name="ic_launcher_background">${ICON_BG}</color>
</resources>
COLEOF
    for ICON_FILE in "$RES/drawable/ic_launcher_foreground.xml" "$RES/drawable/ic_launcher_monochrome.xml"; do
        if [ -f "$ICON_FILE" ]; then
            sed -i "s/android:fillColor=\"#[0-9A-Fa-f]\{6\}\"/android:fillColor=\"${ICON_FG}\"/g" "$ICON_FILE"
        fi
    done
    if [ -d "$ROOT/core" ]; then
        rm -rf "$ANDROID/core"
        cp -r "$ROOT/core" "$ANDROID/core"
    fi
    local GF="$ANDROID/app/build.gradle.kts"
    if [ -f "$GF" ] && ! grep -q "signingConfigs" "$GF"; then
        sed -i '/buildTypes {/i\
    signingConfigs {\
        create("release") {\
            storeFile = signingConfigs.getByName("debug").storeFile\
            storePassword = "android"\
            keyAlias = "androiddebugkey"\
            keyPassword = "android"\
        }\
    }' "$GF"
        sed -i '/isMinifyEnabled = true/a\
            signingConfig = signingConfigs.getByName("release")' "$GF"
    fi
    echo "sdk.dir=$ANDROID_HOME" > "$ANDROID/local.properties"
    echo -e "${GREEN}  ✓${NC}"
}
build_golib() {
    local ANDROID="$ROOT/mobile/android"
    local TARGET="${1:-android/arm64}"
    echo -e "\n  ${CYAN}→ gomobile bind (target: $TARGET)...${NC}"
    cd "$ANDROID/golib"
    go mod tidy 2>/dev/null || true
    MOBILE_VER=$(grep "golang.org/x/mobile" go.mod | head -1 | awk '{print $2}')
    [ -z "$MOBILE_VER" ] && MOBILE_VER="latest"
    go install "golang.org/x/mobile/cmd/gomobile@$MOBILE_VER" 2>/dev/null || go install golang.org/x/mobile/cmd/gomobile@latest
    go install "golang.org/x/mobile/cmd/gobind@$MOBILE_VER" 2>/dev/null || go install golang.org/x/mobile/cmd/gobind@latest
    gomobile init 2>/dev/null || true
    go get golang.org/x/mobile/bind 2>/dev/null || true
    go mod tidy 2>/dev/null || true
    mkdir -p "$ANDROID/app/libs"
    gomobile bind -target="$TARGET" -androidapi=24 -ldflags="-s -w" -o "$ANDROID/app/libs/liblionheart.aar" .
    echo -e "  ${GREEN}✓ .aar ($(ls -lh "$ANDROID/app/libs/liblionheart.aar" | awk '{print $5}'))${NC}"
    cd "$ROOT"
}
build_cli() {
    echo -e "\n${BOLD}[CLI] Сборка...${NC}"
    check_go
    cd "$ROOT/cmd/lionheart"
    go mod tidy 2>/dev/null || true
    PLATFORMS=(
        "linux/amd64" "linux/arm64"
        "darwin/amd64" "darwin/arm64"
        "windows/amd64" "windows/arm64"
        "freebsd/amd64"
    )
    for P in "${PLATFORMS[@]}"; do
        OS="${P%/*}"; ARCH="${P#*/}"; SUFFIX=""
        [ "$OS" = "windows" ] && SUFFIX=".exe"
        NAME="$(human_name "$OS" "$ARCH")${SUFFIX}"
        echo -e "  → ${NAME}..."
        CGO_ENABLED=0 GOOS="$OS" GOARCH="$ARCH" go build -ldflags="-s -w" -o "$OUT/$NAME" . 2>/dev/null || echo -e "    ${YELLOW}⚠ пропущено${NC}"
    done
    cd "$ROOT"
    echo -e "${GREEN}  ✓ CLI → output/${NC}"
}
build_debugapk() {
    echo -e "\n${BOLD}[APK] Android (debug, arm64 only)...${NC}"
    check_go
    find_android_sdk
    if ! find_ndk; then
        echo -e "${RED}❌ Невозможно собрать .aar без NDK. Пропускаю Android.${NC}"
        return 1
    fi
    sync_config
    local ANDROID="$ROOT/mobile/android"
    local AAR="$ANDROID/app/libs/liblionheart.aar"
    local NEED_AAR=0
    if [ ! -f "$AAR" ]; then
        NEED_AAR=1
    else
        local NEWEST_GO=$(find "$ANDROID/golib" "$ANDROID/core" -name "*.go" -newer "$AAR" 2>/dev/null | head -1)
        [ -n "$NEWEST_GO" ] && NEED_AAR=1
    fi
    if [ "$NEED_AAR" = "1" ]; then
        build_golib "android/arm64"
    else
        echo -e "  ${GREEN}✓ .aar актуален, пропускаю gomobile bind${NC}"
    fi
    cd "$ANDROID"
    chmod +x gradlew 2>/dev/null || true
    echo -e "\n  ${CYAN}→ assembleDebug...${NC}"
    ./gradlew assembleDebug --no-daemon
    local DEBUG_APK=$(find . -name "*.apk" -path "*/debug/*" 2>/dev/null | head -1)
    if [ -n "$DEBUG_APK" ]; then
        cp "$DEBUG_APK" "$OUT/lionheart-${VERSION}-android-debug.apk"
        echo -e "  ${GREEN}✓ lionheart-${VERSION}-android-debug.apk ($(ls -lh "$OUT/lionheart-${VERSION}-android-debug.apk" | awk '{print $5}'))${NC}"
    fi
    if command -v adb >/dev/null 2>&1 && adb devices 2>/dev/null | grep -q "device$"; then
        echo -e "\n  → adb install (debug)..."
        adb install -r "$OUT/lionheart-${VERSION}-android-debug.apk" 2>/dev/null && echo -e "  ${GREEN}✓ Установлено на устройство${NC}" || true
    fi
    cd "$ROOT"
}
build_apk() {
    echo -e "\n${BOLD}[APK] Android (release, arm64 only)...${NC}"
    check_go
    find_android_sdk
    if ! find_ndk; then
        echo -e "${RED}❌ Невозможно собрать .aar без NDK. Пропускаю Android.${NC}"
        return 1
    fi
    sync_config
    local ANDROID="$ROOT/mobile/android"
    # arm64 only for release — covers 99%+ of devices, halves .aar size
    build_golib "android/arm64"
    cd "$ANDROID"
    chmod +x gradlew 2>/dev/null || true
    echo -e "\n  ${CYAN}→ assembleRelease...${NC}"
    ./gradlew assembleRelease --no-daemon
    local RELEASE_APK=$(find . -name "*.apk" -path "*/release/*" 2>/dev/null | head -1)
    if [ -n "$RELEASE_APK" ]; then
        cp "$RELEASE_APK" "$OUT/lionheart-${VERSION}-android.apk"
        echo -e "  ${GREEN}✓ lionheart-${VERSION}-android.apk ($(ls -lh "$OUT/lionheart-${VERSION}-android.apk" | awk '{print $5}'))${NC}"
    fi
    if command -v adb >/dev/null 2>&1 && adb devices 2>/dev/null | grep -q "device$"; then
        echo -e "\n  → adb install (release)..."
        adb install -r "$OUT/lionheart-${VERSION}-android.apk" 2>/dev/null && echo -e "  ${GREEN}✓ Установлено${NC}" || true
    fi
    cd "$ROOT"
}
build_apk_fat() {
    echo -e "\n${BOLD}[APK] Android (release, all architectures)...${NC}"
    check_go
    find_android_sdk
    if ! find_ndk; then
        echo -e "${RED}❌ Невозможно собрать .aar без NDK. Пропускаю Android.${NC}"
        return 1
    fi
    sync_config
    local ANDROID="$ROOT/mobile/android"
    build_golib "android"
    cd "$ANDROID"
    chmod +x gradlew 2>/dev/null || true
    echo -e "\n  ${CYAN}→ assembleRelease (fat)...${NC}"
    ./gradlew assembleRelease --no-daemon
    local RELEASE_APK=$(find . -name "*.apk" -path "*/release/*" 2>/dev/null | head -1)
    if [ -n "$RELEASE_APK" ]; then
        cp "$RELEASE_APK" "$OUT/lionheart-${VERSION}-android-universal.apk"
        echo -e "  ${GREEN}✓ lionheart-${VERSION}-android-universal.apk ($(ls -lh "$OUT/lionheart-${VERSION}-android-universal.apk" | awk '{print $5}'))${NC}"
    fi
    cd "$ROOT"
}
case "$MODE" in
    cli)      build_cli ;;
    debugapk) build_debugapk ;;
    apk)      build_apk ;;
    fatapk)   build_apk_fat ;;
    all)      build_cli; build_apk ;;
    *)        echo -e "Использование: ./build.sh [cli|apk|debugapk|fatapk|all]"
              echo ""
              echo "  cli       — только CLI бинарники (все платформы)"
              echo "  debugapk  — debug APK arm64 + adb install (быстро)"
              echo "  apk       — release APK arm64 (оптимальный размер)"
              echo "  fatapk    — release APK все архитектуры (для совместимости)"
              echo "  all       — CLI + release APK arm64"
              exit 1 ;;
esac
echo ""
echo -e "${GREEN}═══════════════════════════════════════${NC}"
echo -e "${GREEN} ✅ Готово! Всё в output/:${NC}"
echo -e "${GREEN}═══════════════════════════════════════${NC}"
ls -lh "$OUT"/ 2>/dev/null
echo ""