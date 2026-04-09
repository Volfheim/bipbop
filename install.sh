#!/usr/bin/env bash
# ==============================================================================
# BIP-BOP SERVER MANAGER — Интерактивное меню управления
# ==============================================================================
# Установка: curl -sL https://raw.githubusercontent.com/Volfheim/bipbop/main/install.sh | sudo bash
# Запуск:    bipbop

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BLUE='\033[0;34m'
MAGENTA='\033[0;35m'
BOLD='\033[1m'
DIM='\033[2m'
NC='\033[0m'

INSTALL_DIR="/usr/local/bin"
CONF_DIR="/etc/volfheim"
CONF_FILE="$CONF_DIR/config.env"
BIN_NAME="volfheim-server"
SVC_NAME="volfheim.service"
GITHUB_REPO="Volfheim/bipbop"

if [[ $EUID -ne 0 ]]; then
   echo -e "${RED}Запустите от root: sudo bipbop${NC}" 
   exit 1
fi

# ==============================================================================
# Баннер
# ==============================================================================
print_banner() {
    clear
    echo -e "${CYAN}${BOLD}"
    echo "  ╔══════════════════════════════════════╗"
    echo "  ║         ⚡ BIP-BOP v2.0 ⚡           ║"
    echo "  ║     Telemost DPI Bypass Server       ║"
    echo "  ╚══════════════════════════════════════╝"
    echo -e "${NC}"
}

get_room_url() {
    [[ -f "$CONF_FILE" ]] && source "$CONF_FILE" && echo "$VOLFHEIM_ROOM_URL" || echo ""
}

get_password() {
    [[ -f "$CONF_FILE" ]] && source "$CONF_FILE" && echo "$VOLFHEIM_PASSWORD" || echo ""
}

server_status() {
    if systemctl is-active --quiet "$SVC_NAME" 2>/dev/null; then
        echo -e "${GREEN}● Сервер работает${NC}"
    else
        echo -e "${RED}○ Сервер остановлен${NC}"
    fi
}

# ==============================================================================
# 1. Установка / Обновление сервера
# ==============================================================================
install_server() {
    echo -e "\n${CYAN}>>> Установка / Обновление сервера...${NC}"
    mkdir -p "$CONF_DIR"

    local room_url
    room_url=$(get_room_url)

    if [[ -z "$room_url" ]]; then
        local password
        password=$(openssl rand -hex 16)
        
        echo -e "${YELLOW}"
        echo "  Для маскировки под звонок Яндекс Телемост:"
        echo "  1. Зайдите на https://telemost.yandex.ru"
        echo "  2. Нажмите 'Запланировать встречу'"
        echo "  3. Скопируйте Вечную Ссылку"
        echo -e "${NC}"
        read -r -p "  Ссылка на Телемост: " input_url

        if [[ -z "$input_url" || ! "$input_url" == *"telemost.yandex.ru"* ]]; then
            echo -e "${RED}  [✗] Некорректная ссылка.${NC}"
            read -r -p "  Enter..."
            return
        fi

        room_url="$input_url"
        echo "VOLFHEIM_PASSWORD=$password" > "$CONF_FILE"
        echo "VOLFHEIM_ROOM_URL=$room_url" >> "$CONF_FILE"
        chmod 600 "$CONF_FILE"
        echo -e "${GREEN}  [+] Конфиг создан${NC}"
    else
        echo -e "${GREEN}  [+] Конфиг найден: $room_url${NC}"
    fi

    echo -e "${CYAN}  Скачивание бинарника...${NC}"
    local URL="https://raw.githubusercontent.com/$GITHUB_REPO/main/bipbop-server/volfheim-linux-amd64"
    curl -sL "$URL" -o "/tmp/$BIN_NAME"
    
    if [[ ! -s "/tmp/$BIN_NAME" || $(stat -c%s "/tmp/$BIN_NAME") -lt 100000 ]]; then
        echo -e "${RED}  [✗] Ошибка загрузки. Проверьте сеть.${NC}"
        read -r -p "  Enter..."
        return
    fi

    mv -f "/tmp/$BIN_NAME" "$INSTALL_DIR/$BIN_NAME"
    chmod +x "$INSTALL_DIR/$BIN_NAME"

    # Создаём systemd-сервис
    cat <<EOF > /etc/systemd/system/$SVC_NAME
[Unit]
Description=Bip-Bop Telemost DPI Bypass Server
After=network.target

[Service]
Type=simple
User=root
LimitNOFILE=51200
EnvironmentFile=$CONF_FILE
ExecStart=$INSTALL_DIR/$BIN_NAME server --listen \${VOLFHEIM_ROOM_URL} --password \${VOLFHEIM_PASSWORD} --data $CONF_DIR
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl stop "$SVC_NAME" 2>/dev/null || true
    pkill -9 -f "$BIN_NAME" 2>/dev/null || true
    sleep 1

    systemctl enable "$SVC_NAME"
    systemctl start "$SVC_NAME"
    sleep 2

    if systemctl is-active --quiet "$SVC_NAME"; then
        echo -e "${GREEN}  [+] Сервер запущен и в автозагрузке!${NC}"
    else
        echo -e "${RED}  [✗] Ошибка запуска. Логи: journalctl -u $SVC_NAME -n 20${NC}"
    fi

    # Создаём симлинк bipbop → install.sh
    if [[ ! -f "$INSTALL_DIR/bipbop" ]]; then
        local SCRIPT_PATH
        SCRIPT_PATH=$(readlink -f "$0")
        cp "$SCRIPT_PATH" "$INSTALL_DIR/bipbop" 2>/dev/null || true
        chmod +x "$INSTALL_DIR/bipbop" 2>/dev/null || true
        echo -e "${GREEN}  [+] Команда 'bipbop' установлена${NC}"
    fi

    read -r -p "  Enter..."
}

# ==============================================================================
# 2. Добавить клиента
# ==============================================================================
add_client() {
    if [[ ! -f "$INSTALL_DIR/$BIN_NAME" || ! -f "$CONF_FILE" ]]; then
        echo -e "${RED}  Сервер не установлен! Выберите пункт 1.${NC}"
        read -r -p "  Enter..."
        return
    fi

    echo -e "\n${CYAN}>>> Добавление нового клиента${NC}\n"
    read -r -p "  Имя клиента: " client_name
    
    if [[ -z "$client_name" ]]; then
        echo -e "${RED}  Имя не может быть пустым.${NC}"
        read -r -p "  Enter..."
        return
    fi

    local room_url
    room_url=$(get_room_url)

    "$INSTALL_DIR/$BIN_NAME" client add --name "$client_name" --listen "$room_url" --data "$CONF_DIR"

    echo ""
    read -r -p "  Enter..."
}

# ==============================================================================
# 3. Список клиентов
# ==============================================================================
list_clients() {
    echo -e "\n${CYAN}>>> Клиенты${NC}"
    
    if [[ ! -f "$INSTALL_DIR/$BIN_NAME" ]]; then
        echo -e "${RED}  Сервер не установлен.${NC}"
        read -r -p "  Enter..."
        return
    fi

    "$INSTALL_DIR/$BIN_NAME" client list --data "$CONF_DIR"

    echo ""
    read -r -p "  Enter..."
}

# ==============================================================================
# 4. Отозвать клиента
# ==============================================================================
revoke_client() {
    if [[ ! -f "$INSTALL_DIR/$BIN_NAME" ]]; then
        echo -e "${RED}  Сервер не установлен.${NC}"
        read -r -p "  Enter..."
        return
    fi

    echo -e "\n${CYAN}>>> Отзыв клиента${NC}\n"
    
    # Показать список
    "$INSTALL_DIR/$BIN_NAME" client list --data "$CONF_DIR"
    
    echo ""
    read -r -p "  ID клиента для отзыва: " cid
    
    if [[ -z "$cid" ]]; then
        echo -e "${RED}  ID не указан.${NC}"
        read -r -p "  Enter..."
        return
    fi

    "$INSTALL_DIR/$BIN_NAME" client revoke --id "$cid" --data "$CONF_DIR"

    echo ""
    read -r -p "  Enter..."
}

# ==============================================================================
# 5. Удалить клиента
# ==============================================================================
delete_client() {
    if [[ ! -f "$INSTALL_DIR/$BIN_NAME" ]]; then
        echo -e "${RED}  Сервер не установлен.${NC}"
        read -r -p "  Enter..."
        return
    fi

    echo -e "\n${CYAN}>>> Удаление клиента${NC}\n"
    
    "$INSTALL_DIR/$BIN_NAME" client list --data "$CONF_DIR"
    
    echo ""
    read -r -p "  ID клиента для удаления: " cid
    
    if [[ -z "$cid" ]]; then
        echo -e "${RED}  ID не указан.${NC}"
        read -r -p "  Enter..."
        return
    fi

    echo -e "${RED}  Удалить клиента $cid навсегда? (y/n)${NC}"
    read -r confirm
    if [[ "$confirm" == "y" || "$confirm" == "Y" ]]; then
        "$INSTALL_DIR/$BIN_NAME" client delete --id "$cid" --data "$CONF_DIR"
    else
        echo -e "${YELLOW}  Отменено.${NC}"
    fi

    echo ""
    read -r -p "  Enter..."
}

# ==============================================================================
# 6. Логи сервера
# ==============================================================================
show_logs() {
    echo -e "\n${CYAN}>>> Последние 30 строк логов (Ctrl+C для выхода)${NC}\n"
    journalctl -u "$SVC_NAME" -n 30 --no-pager
    echo ""
    read -r -p "  Enter..."
}

# ==============================================================================
# 7. Перезапуск сервера
# ==============================================================================
restart_server() {
    echo -e "\n${CYAN}>>> Перезапуск сервера...${NC}"
    systemctl restart "$SVC_NAME"
    sleep 2
    if systemctl is-active --quiet "$SVC_NAME"; then
        echo -e "${GREEN}  [+] Сервер перезапущен!${NC}"
    else
        echo -e "${RED}  [✗] Ошибка. journalctl -u $SVC_NAME -n 20${NC}"
    fi
    read -r -p "  Enter..."
}

# ==============================================================================
# 8. Удаление всего
# ==============================================================================
uninstall_all() {
    echo -e "\n${RED}${BOLD}  ⚠ УДАЛИТЬ СЕРВЕР И ВСЕ КЛЮЧИ? (y/n)${NC}"
    read -r confirm
    if [[ "$confirm" == "y" || "$confirm" == "Y" ]]; then
        systemctl stop "$SVC_NAME" 2>/dev/null
        systemctl disable "$SVC_NAME" 2>/dev/null
        rm -f "/etc/systemd/system/$SVC_NAME"
        systemctl daemon-reload
        rm -f "$INSTALL_DIR/$BIN_NAME"
        rm -f "$INSTALL_DIR/bipbop"
        rm -rf "$CONF_DIR"
        echo -e "${GREEN}  [+] Всё удалено.${NC}"
    else
        echo -e "${YELLOW}  Отменено.${NC}"
    fi
    read -r -p "  Enter..."
}

# ==============================================================================
# Главное меню
# ==============================================================================
while true; do
    print_banner
    echo -e "  $(server_status)"
    echo ""
    echo -e "  ${BOLD}Сервер${NC}"
    echo -e "  ${GREEN}1)${NC} Установить / Обновить"
    echo -e "  ${GREEN}2)${NC} Перезапустить"
    echo -e "  ${GREEN}3)${NC} Логи"
    echo ""
    echo -e "  ${BOLD}Клиенты${NC}"
    echo -e "  ${CYAN}4)${NC} Добавить клиента"
    echo -e "  ${CYAN}5)${NC} Список клиентов"
    echo -e "  ${YELLOW}6)${NC} Отозвать ключ"
    echo -e "  ${RED}7)${NC} Удалить клиента"
    echo ""
    echo -e "  ${DIM}8) Удалить всё${NC}"
    echo -e "  ${DIM}0) Выход${NC}"
    echo ""
    read -r -p "  > " choice

    case $choice in
        1) install_server ;;
        2) restart_server ;;
        3) show_logs ;;
        4) add_client ;;
        5) list_clients ;;
        6) revoke_client ;;
        7) delete_client ;;
        8) uninstall_all ;;
        0) echo -e "\n${GREEN}  До встречи!${NC}\n"; exit 0 ;;
        *) echo -e "${RED}  ?${NC}"; sleep 0.5 ;;
    esac
done
