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
    [[ -f "$CONF_FILE" ]] && grep VOLFHEIM_ROOM_URL "$CONF_FILE" | cut -d '=' -f 2 || echo ""
}

install_server() {
    echo -e "\n${CYAN}>>> Установка / Обновление Volfheim Server...${NC}"
    mkdir -p "$CONF_DIR"

    local room_url=$(get_room_url)
    
    if [[ -z "$room_url" ]]; then
        echo -e "${YELLOW}Введите Вечную Ссылку из Яндекс Телемост:${NC}"
        read -r -p "Ссылка: " room_url
        if [[ -z "$room_url" || ! "$room_url" == *"telemost.yandex.ru"* ]]; then
            echo -e "${RED}[✗] Некорректная ссылка.${NC}"
            return
        fi
        password=$(openssl rand -hex 16)
        echo "VOLFHEIM_PASSWORD=$password" > "$CONF_FILE"
        echo "VOLFHEIM_ROOM_URL=$room_url" >> "$CONF_FILE"
        chmod 600 "$CONF_FILE"
    fi

    echo -e "${CYAN}>>> Загрузка бинарника...${NC}"
    curl -sL "https://raw.githubusercontent.com/$GITHUB_REPO/main/bipbop-server/volfheim-linux-amd64" -o "$INSTALL_DIR/$BIN_NAME"
    chmod +x "$INSTALL_DIR/$BIN_NAME"

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

    echo -e "${CYAN}>>> Настройка файрвола (открытие порта 80)...${NC}"
    if command -v ufw >/dev/null; then
        ufw allow 80/tcp >/dev/null
    fi
    if command -v iptables >/dev/null; then
        iptables -I INPUT -p tcp --dport 80 -j ACCEPT 2>/dev/null
    fi

    systemctl daemon-reload
    systemctl enable --now "$SVC_NAME"
    systemctl restart "$SVC_NAME"
    echo -e "${GREEN}[+] Сервер запущен на порту 80.${NC}"
    read -r -p "Enter..."
}

add_client() {
    echo -e "\n${CYAN}>>> Создание ключа${NC}"
    read -r -p "Имя клиента: " client_name
    [[ -z "$client_name" ]] && return

    password=$(grep VOLFHEIM_PASSWORD "$CONF_FILE" | cut -d '=' -f 2)
    room_url=$(get_room_url)
    vps_ip=$(curl -s https://api.ipify.org)
    
    raw_data="$room_url|$password|$vps_ip"
    smart_key=$(echo -n "$raw_data" | base64 | tr '+/' '-_' | tr -d '=' | tr -d '\n')

    echo "[$client_name] : $smart_key" >> "$CLIENTS_FILE"
    echo -e "${GREEN}[+] Добавлен: $client_name${NC}"
    echo -e "${YELLOW}Ключ:${NC} $smart_key"
    read -r -p "Enter..."
}

list_clients() {
    echo -e "\n${CYAN}>>> Список клиентов:${NC}"
    if [[ -f "$CLIENTS_FILE" ]]; then
        cat "$CLIENTS_FILE" | column -t -s ':'
    else
        echo "Клиентов пока нет."
    fi
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
        2) systemctl status "$SVC_NAME" ;;
        3) add_client ;;
        4) list_clients ;;
        5) delete_client ;;
        6) uninstall_server ;;
        0) exit 0 ;;
        *) echo "Неверный выбор." ; sleep 1 ;;
    esac
done
