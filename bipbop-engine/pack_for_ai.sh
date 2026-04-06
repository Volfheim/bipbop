#!/bin/bash
# ═══════════════════════════════════════════════════════
#  pack_for_ai.sh — упаковывает код для отправки в ИИ-чат
# ═══════════════════════════════════════════════════════
#
#  ./pack_for_ai.sh          ← всё нужное для разработки
#  ./pack_for_ai.sh android  ← только Android-код
#  ./pack_for_ai.sh core     ← только core + CLI
#
#  Создаёт: lionheart-for-ai.zip
#
# ═══════════════════════════════════════════════════════
set -e

ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT"

MODE="${1:-all}"
OUTFILE="$ROOT/lionheart-for-ai.zip"

# Удаляем старый если есть
rm -f "$OUTFILE"

echo "📦 Упаковка кода для ИИ..."

# Общие исключения для zip
EXCLUDES=(
    -x '*.apk'
    -x '*.aar'
    -x '*.exe'
    -x '*.zip'
    -x '*.tar.gz'
    -x 'output/*'
    -x 'dist/*'
    -x '*/.gradle/*'
    -x '*/build/*'
    -x '*/.idea/*'
    -x '*.iml'
    -x 'local.properties'
    -x 'gradlew'
    -x 'gradlew.bat'
    -x '*/gradle-wrapper.jar'
    -x 'mobile/android/app/libs/*'
    -x '__pycache__/*'
    -x '*/.git/*'
)

case "$MODE" in
    android)
        echo "  Режим: только Android"
        zip -r "$OUTFILE" \
            config/ \
            core/ \
            mobile/android/app/src/ \
            mobile/android/app/build.gradle.kts \
            mobile/android/app/proguard-rules.pro \
            mobile/android/golib/ \
            mobile/android/anet-stub/ \
            mobile/android/core/ \
            mobile/android/build.gradle.kts \
            mobile/android/settings.gradle.kts \
            mobile/android/gradle.properties \
            mobile/android/build.sh \
            "${EXCLUDES[@]}" \
            2>/dev/null || true
        ;;
    core)
        echo "  Режим: core + CLI"
        zip -r "$OUTFILE" \
            config/ \
            core/ \
            cmd/ \
            "${EXCLUDES[@]}" \
            2>/dev/null || true
        ;;
    all|*)
        echo "  Режим: всё"
        zip -r "$OUTFILE" \
            config/ \
            core/ \
            cmd/ \
            mobile/android/app/src/ \
            mobile/android/app/build.gradle.kts \
            mobile/android/app/proguard-rules.pro \
            mobile/android/golib/ \
            mobile/android/anet-stub/ \
            mobile/android/core/ \
            mobile/android/build.gradle.kts \
            mobile/android/settings.gradle.kts \
            mobile/android/gradle.properties \
            mobile/golib/ \
            mobile/anet-stub/ \
            build.sh \
            README.md \
            "${EXCLUDES[@]}" \
            2>/dev/null || true
        ;;
esac

SIZE=$(ls -lh "$OUTFILE" | awk '{print $5}')
echo ""
echo "✅ $OUTFILE ($SIZE)"
echo ""
echo "Отправь этот файл в чат с ИИ. Содержит:"

case "$MODE" in
    android) echo "  • config/ — настройки, переводы"; echo "  • core/ — ядро"; echo "  • mobile/android/ — Kotlin, Gradle, golib" ;;
    core)    echo "  • config/ — настройки"; echo "  • core/ — ядро"; echo "  • cmd/ — CLI" ;;
    *)       echo "  • config/ — настройки, переводы"; echo "  • core/ — ядро"; echo "  • cmd/ — CLI"; echo "  • mobile/ — Android + golib" ;;
esac
echo ""