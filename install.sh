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
    echo "        BIP-BOP Server Manager v1.0         "
    echo -e "${NC}================================================="
}

get_room_url() {
    if [[ -f "$CONF_FILE" ]]; then
        source "$CONF_FILE"
        echo "$VOLFHEIM_ROOM_URL"
    else
        echo ""
    fi
}

install_server() {
    echo -e "\n${CYAN}>>> Установка / Обновление Volfheim Server...${NC}"
    mkdir -p "$CONF_DIR"

    local room_url
    room_url=$(get_room_url)
    local password

    if [[ -z "$room_url" ]]; then
        password=$(openssl rand -hex 16)
        echo -e "${YELLOW}Введите Вечную Ссылку из Яндекс Телемост:${NC}"
        read -r -p "Ссылка: " input_url
        room_url=${input_url}

        if [[ -z "$room_url" || ! "$room_url" == *"telemost.yandex.ru"* ]]; then
            echo -e "${RED}[✗] Некорректная ссылка.${NC}"
            exit 1
        fi

        echo "VOLFHEIM_PASSWORD=$password" > "$CONF_FILE"
        echo "VOLFHEIM_ROOM_URL=$room_url" >> "$CONF_FILE"
        chmod 600 "$CONF_FILE"
    fi

    echo -e "${CYAN}>>> Загрузка бинарника с GitHub...${NC}"
    local URL="https://raw.githubusercontent.com/$GITHUB_REPO/main/bipbop-server/volfheim-linux-amd64"
    curl -sL "$URL" -o "/tmp/$BIN_NAME"
    
    if [[ ! -s "/tmp/$BIN_NAME" ]]; then
        echo -e "${RED}[✗] Ошибка загрузки.${NC}"
        return
    fi

    mv -f "/tmp/$BIN_NAME" "$INSTALL_DIR/$BIN_NAME"
    chmod +x "$INSTALL_DIR/$BIN_NAME"

    cat <<EOF > /etc/systemd/system/$SVC_NAME
[Unit]
Description=Bip-Bop Telemost DPI Bypass Server
After=network.target

[Service]
Type=simple
User=root
LimitNOFILE=51200
EnvironmentFile=$CONF_FILE
ExecStart=$INSTALL_DIR/$BIN_NAME server --listen \${VOLFHEIM_ROOM_URL} --password \${VOLFHEIM_PASSWORD}
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl stop "$SVC_NAME" 2>/dev/null || true
    pkill -9 -f "$BIN_NAME" 2>/dev/null || true
    
    systemctl enable "$SVC_NAME"
    systemctl start "$SVC_NAME"

    sleep 2
    if systemctl is-active --quiet "$SVC_NAME"; then
        echo -e "${GREEN}[+] Готово! Сервер работает.${NC}"
    else
        echo -e "${RED}[✗] Ошибка запуска. Проверьте: journalctl -u $SVC_NAME -n 20${NC}"
    fi
    read -r -p "Enter..."
}

add_client() {
    echo -e "\n${CYAN}>>> Создание ключа для клиента${NC}"
    read -r -p "Имя: " client_name
    password=$(grep VOLFHEIM_PASSWORD "$CONF_FILE" | cut -d '=' -f 2)
    room_url=$(get_room_url)
    raw_data="$room_url|$password"
    smart_key=$(echo -n "$raw_data" | base64 | tr '+/' '-_' | tr -d '=' | tr -d '\n')
    echo "[$client_name] : $smart_key" >> "$CLIENTS_FILE"
    echo -e "${YELLOW}$smart_key${NC}"
    read -r -p "Enter..."
}

while true; do
    print_banner
    echo -e " 1) Установить / Обновить"
    echo -e " 2) Добавить клиента"
    echo -e " 3) Список клиентов"
    echo -e " 0) Выход"
    read -r -p "Action: " choice
    case $choice in
        1) install_server ;;
        2) add_client ;;
        3) [[ -f "$CLIENTS_FILE" ]] && cat "$CLIENTS_FILE" || echo "Пусто"; read -r -p "Enter..." ;;
        0) exit 0 ;;
    esac
done
