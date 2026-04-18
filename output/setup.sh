#!/usr/bin/env bash
# ==============================================================================
# VOLFHEIM BIP-BOP SERVER SETUP SCRIPT v1.0
# ==============================================================================

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

INSTALL_DIR="/usr/local/bin"
CONF_DIR="/etc/volfheim"
CONF_FILE="$CONF_DIR/config.env"
CLIENTS_FILE="$CONF_DIR/clients.txt"
BIN_NAME="volfheim-server"
SVC_NAME="volfheim.service"
GITHUB_REPO="Volfheim/bipbop"

if [[ $EUID -ne 0 ]]; then
   echo -e "${RED}Ошибка: Этот скрипт должен запускаться от имени root (sudo).${NC}" 
   exit 1
fi

print_banner() {
    clear
    echo -e "${CYAN}${BOLD}"
    echo "  _   _       _  __ _          _           "
    echo " | | | |     | |/ _| |        (_)          "
    echo " | | | | ___ | | |_| |__   ___ _ _ __ ___  "
    echo " | | | |/ _ \| |  _| '_ \ / _ \ | '_ \` _ \ "
    echo " \ \_/ / (_) | | | | | | |  __/ | | | | | |"
    echo "  \___/ \___/|_|_| |_| |_|\___|_|_| |_| |_|"
    echo "                                           "
    echo "        BIP-BOP Server Manager v4.5"
    echo -e "${YELLOW}        Version: 4.5-GOLDEN (VideoChannel Edition)${NC}"
    echo -e "${NC}================================================="
}

get_room_url() {
    [[ -f "$CONF_FILE" ]] && grep VOLFHEIM_ROOM_URL "$CONF_FILE" | cut -d '=' -f 2 || echo ""
}

install_server() {
    echo -e "\n${CYAN}>>> Установка / Обновление Volfheim Server...${NC}"
    mkdir -p "$CONF_DIR"

    echo -e "${CYAN}>>> Подготовка бинарника...${NC}"
    rm -f "$INSTALL_DIR/$BIN_NAME"
    if [[ -f "./volfheim-linux-amd64" ]]; then
        echo -e "${GREEN}[+] Используем локальный бинарник из текущей папки.${NC}"
        cp "./volfheim-linux-amd64" "$INSTALL_DIR/$BIN_NAME"
    else
        echo -e "${YELLOW}[!] Локальный бинарник не найден, скачиваем из GitHub...${NC}"
        curl -sL "https://raw.githubusercontent.com/$GITHUB_REPO/main/bipbop-server/volfheim-linux-amd64?v=$(date +%s)" -o "$INSTALL_DIR/$BIN_NAME"
    fi
    chmod +x "$INSTALL_DIR/$BIN_NAME"

    local force_gen=false
    if [[ -f "$CONF_FILE" ]]; then
        # Читаем старый конфиг для проверки
        source "$CONF_FILE"
        if [[ -z "$SMART_KEY" || "$VOLFHEIM_ROOM_URL" == *"telemost.yandex.ru"* ]]; then
            force_gen=true
        fi
    fi

    if [[ ! -f "$CONF_FILE" || "$force_gen" == true ]]; then
        echo -e "${CYAN}>>> Генерация конфигурации Jazz Room...${NC}"
        $INSTALL_DIR/$BIN_NAME gen > "$CONF_FILE"
        chmod 600 "$CONF_FILE"
    else
        echo -e "${CYAN}>>> Конфигурация уже существует, используем ее...${NC}"
    fi

    source "$CONF_FILE"
    echo -e "\n${YELLOW}┌─── SMART KEY ───────────────────────────────────────────────┐${NC}"
    echo -e "${YELLOW}│${NC} ${GREEN}${SMART_KEY}${NC}"
    echo -e "${YELLOW}└─────────────────────────────────────────────────────────────┘${NC}\n"

    cat <<EOF > /etc/systemd/system/$SVC_NAME
[Unit]
Description=Bip-Bop Telemost Proxy
After=network.target

[Service]
Type=simple
User=root
EnvironmentFile=$CONF_FILE
ExecStart=$INSTALL_DIR/$BIN_NAME server --listen \${VOLFHEIM_ROOM_URL} --password \${VOLFHEIM_PASSWORD}
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable --now "$SVC_NAME"
    systemctl restart "$SVC_NAME"
    echo -e "${GREEN}[+] Сервер запущен.${NC}"
    read -r -p "Enter..."
}

add_client() {
    echo -e "\n${CYAN}>>> Создание ключа${NC}"
    read -r -p "Имя клиента: " client_name
    [[ -z "$client_name" ]] && return

    password=$(grep VOLFHEIM_PASSWORD "$CONF_FILE" | cut -d '=' -f 2)
    room_url=$(get_room_url)
    
    raw_data="$room_url|$password"
    smart_key=$(echo -n "$raw_data" | base64 | tr '+/' '-_' | tr -d '=' | tr -d '\n')

    echo "[$client_name] : $smart_key" >> "$CLIENTS_FILE"
    echo -e "${GREEN}[+] Добавлен: $client_name${NC}"
    echo -e "${YELLOW}Ключ:${NC} $smart_key"
    read -r -p "Enter..."
}

list_clients() {
    echo -e "\n${CYAN}>>> Список клиентов:${NC}"
    if [[ -f "$CLIENTS_FILE" ]]; then
        echo -e "${YELLOW}Имя | Ключ${NC}"
        echo "-------------------------------------------------"
        cat "$CLIENTS_FILE"
    else
        echo "Клиентов пока нет."
    fi
    echo "-------------------------------------------------"
    read -r -p "Enter..."
}

delete_client() {
    echo -e "\n${CYAN}>>> Удаление клиента${NC}"
    read -r -p "Введите имя (или часть имени): " name
    [[ -z "$name" ]] && return
    
    if grep -q "\[$name\]" "$CLIENTS_FILE"; then
        sed -i "/\[$name\]/d" "$CLIENTS_FILE"
        echo -e "${GREEN}[+] Удалено.${NC}"
    else
        echo -e "${RED}[✗] Не найдено.${NC}"
    fi
    read -r -p "Enter..."
}

uninstall_server() {
    echo -e "${RED}Удалить сервер и ВСЕ ключи? (y/n)${NC}"
    read -r -p "> " confirm
    if [[ "$confirm" == "y" ]]; then
        systemctl stop "$SVC_NAME" 2>/dev/null
        systemctl disable "$SVC_NAME" 2>/dev/null
        rm -rf "$CONF_DIR"
        rm -f "/etc/systemd/system/$SVC_NAME"
        rm -f "$INSTALL_DIR/$BIN_NAME"
        echo -e "${GREEN}[+] Всё удалено.${NC}"
    fi
    read -r -p "Enter..."
}

while true; do
    print_banner
    echo -e " 1) Установить / Обновить"
    echo -e " 2) Статус сервера"
    echo -e " 3) Добавить клиента"
    echo -e " 4) Список клиентов"
    echo -e " 5) Удалить клиента"
    echo -e " 6) Удалить сервер (Полная очистка)"
    echo -e " 0) Выход"
    echo -e "-------------------------------------------------"
    read -r -p "Выбор: " choice
    case $choice in
        1) install_server ;;
        2) systemctl status "$SVC_NAME"; echo; read -r -p "Нажмите Enter для возврата в меню..." ;;
        3) add_client ;;
        4) list_clients ;;
        5) delete_client ;;
        6) uninstall_server ;;
        0) exit 0 ;;
        *) echo "Неверный выбор." ; sleep 1 ;;
    esac
done
