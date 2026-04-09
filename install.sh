#!/usr/bin/env bash
# ==============================================================================
# VOLFHEIM BIP-BOP SERVER SETUP SCRIPT
# ==============================================================================

# Цвета для вывода
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

# Рабочие директории
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

# ==============================================================================
# Вспомогательные функции
# ==============================================================================
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

# ==============================================================================
# 1. Установка и обновление сервера
# ==============================================================================
install_server() {
    echo -e "\n${CYAN}>>> Установка / Обновление Volfheim Server...${NC}"
    mkdir -p "$CONF_DIR"

    # Чтение или создание конфигурации
    local room_url
    room_url=$(get_room_url)
    local password

    if [[ -z "$room_url" ]]; then
        # Генерация 16-байтного случайного пароля в HEX
        password=$(openssl rand -hex 16)
        
        echo -e "${YELLOW}Для маскировки сервера под звонок Yandex Telemost, вам необходимо создать встречу в Яндексе.${NC}"
        echo -e "${YELLOW}Перейдите на https://telemost.yandex.ru, нажмите 'Запланировать' и скопируйте Вечную Ссылку.${NC}"
        read -r -p "Ссылка на Телемост (Room URL): " input_url
        room_url=${input_url}

        if [[ -z "$room_url" || ! "$room_url" == *"telemost.yandex.ru"* ]]; then
            echo -e "${RED}[✗] Некорректная ссылка. Установка прервана.${NC}"
            exit 1
        fi

        # Сохранение конфига
        echo "VOLFHEIM_PASSWORD=$password" > "$CONF_FILE"
        echo "VOLFHEIM_ROOM_URL=$room_url" >> "$CONF_FILE"
        chmod 600 "$CONF_FILE"
        echo -e "${GREEN}[+] Конфигурация создана (Комната: $room_url)${NC}"
    else
        echo -e "${GREEN}[+] Найдена существующая конфигурация (Комната: $room_url)${NC}"
    fi

    echo -e "${CYAN}>>> Загрузка последней версии с GitHub...${NC}"
    # Используем прямую ссылку на бинарник в папке bipbop-server, так как он закоммичен в репозиторий
    local DOWNLOAD_URL="https://raw.githubusercontent.com/$GITHUB_REPO/main/bipbop-server/volfheim-linux-amd64"
    
    # Скачивание и установка
    curl -sL "$DOWNLOAD_URL" -o "/tmp/$BIN_NAME"
    if [[ ! -s "/tmp/$BIN_NAME" || $(stat -c%s "/tmp/$BIN_NAME") -lt 100000 ]]; then
        echo -e "${RED}[✗] Ошибка скачивания файла. Ссылка недоступна или файл поврежден.${NC}"
        read -r -p "Нажмите Enter для продолжения..."
        return
    fi

    mv -f "/tmp/$BIN_NAME" "$INSTALL_DIR/$BIN_NAME"
    chmod +x "$INSTALL_DIR/$BIN_NAME"

    echo -e "${CYAN}>>> Настройка службы SystemD...${NC}"
    cat <<EOF > /etc/systemd/system/$SVC_NAME
[Unit]
Description=Volfheim Custom VPN Core
After=network.target

[Service]
Type=simple
User=root
# Лимит файлов для высоконагруженных сокетов
LimitNOFILE=51200
EnvironmentFile=$CONF_FILE
ExecStart=$INSTALL_DIR/$BIN_NAME server --listen \${VOLFHEIM_ROOM_URL} --password \${VOLFHEIM_PASSWORD}
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    
    # Жесткая остановка старых и новых служб для освобождения порта
    systemctl stop "$SVC_NAME" 2>/dev/null || true
    systemctl stop lionheart.service 2>/dev/null || true
    systemctl disable lionheart.service 2>/dev/null || true
    pkill -9 -f "lionheart" 2>/dev/null || true
    pkill -9 -f "volfheim-server" 2>/dev/null || true
    sleep 2

    systemctl enable "$SVC_NAME"
    systemctl start "$SVC_NAME"

    # Проверка статуса
    sleep 2
    if systemctl is-active --quiet "$SVC_NAME"; then
        echo -e "${GREEN}[+] Успешно! Сервер запущен и добавлен в автозагрузку.${NC}"
        echo -e "Теперь вы можете добавить клиентов через меню."
    else
        echo -e "${RED}[✗] Сервер не запустился. Проверьте логи: journalctl -u $SVC_NAME -n 20${NC}"
    fi

    read -r -p "Нажмите Enter для продолжения..."
}

# ==============================================================================
# 2. Добавление клиента
# ==============================================================================
add_client() {
    if [[ ! -f "$INSTALL_DIR/$BIN_NAME" || ! -f "$CONF_FILE" ]]; then
        echo -e "${RED}Сервер еще не установлен! Сначала выполните Установку (Опция 1).${NC}"
        read -r -p "Нажмите Enter для возврата..."
        return
    fi
    
    echo -e "\n${CYAN}>>> Создание нового клиента${NC}"
    read -r -p "Введите имя клиента (например: Phone-Oleg): " client_name
    
    if [[ -z "$client_name" ]]; then
        echo -e "${RED}Имя не может быть пустым.${NC}"
        read -r -p "Нажмите Enter для продолжения..."
        return
    fi

    # Генерация ключа через сам сервер
    local password
    password=$(grep VOLFHEIM_PASSWORD "$CONF_FILE" | cut -d '=' -f 2)
    local room_url
    room_url=$(get_room_url)
    
    echo -e "Генерация ключа для ${YELLOW}$client_name${NC}..."
    
    # Формат BipBop (Yandex Telemost): base64_url("RoomURL|password")
    local raw_data="$room_url|$password"
    # Формируем URL-safe Base64 без паддинга
    local smart_key
    smart_key=$(echo -n "$raw_data" | base64 | tr '+/' '-_' | tr -d '=' | tr -d '\n')
    
    # Сохраняем в список
    local date_str
    date_str=$(date '+%Y-%m-%d %H:%M')
    echo "[$date_str] $client_name : $smart_key" >> "$CLIENTS_FILE"
    
    echo -e "\n${GREEN}[+] Клиент '${client_name}' успешно создан!${NC}"
    echo -e "Ваш Smart-Key:"
    echo -e "──────────────────────────────────────────────────────────"
    echo -e "${YELLOW}$smart_key${NC}"
    echo -e "──────────────────────────────────────────────────────────"
    echo -e "Используйте этот ключ в приложении BipBop Android."
    
    read -r -p "Нажмите Enter для продолжения..."
}

# ==============================================================================
# 3. Список клиентов
# ==============================================================================
list_clients() {
    echo -e "\n${CYAN}>>> Список зарегистрированных клиентов${NC}"
    
    if [[ ! -f "$CLIENTS_FILE" ]]; then
        echo -e "${YELLOW}База клиентов пока пуста. Вы еще никого не добавили.${NC}"
    else
        echo -e "──────────────────────────────────────────────────────────"
        cat "$CLIENTS_FILE"
        echo -e "──────────────────────────────────────────────────────────"
    fi
    
    read -r -p "Нажмите Enter для продолжения..."
}

# ==============================================================================
# 4. Удаление сервера
# ==============================================================================
uninstall_server() {
    echo -e "\n${RED}ВЫ УВЕРЕНЫ, ЧТО ХОТИТЕ УДАЛИТЬ СЕРВЕР И ВСЕ КЛЮЧИ? (y/n)${NC}"
    read -r confirm
    if [[ "$confirm" == "y" || "$confirm" == "Y" ]]; then
        systemctl stop "$SVC_NAME" 2>/dev/null
        systemctl disable "$SVC_NAME" 2>/dev/null
        rm -f "/etc/systemd/system/$SVC_NAME"
        systemctl daemon-reload
        rm -f "$INSTALL_DIR/$BIN_NAME"
        rm -rf "$CONF_DIR"
        echo -e "${GREEN}[+] Сервер и клиентские данные успешно удалены.${NC}"
    else
        echo -e "${YELLOW}Удаление отменено.${NC}"
    fi
    read -r -p "Нажмите Enter для продолжения..."
}

# ==============================================================================
# Основной цикл меню
# ==============================================================================
while true; do
    print_banner
    echo -e " 1) Установить / Обновить сервер"
    echo -e " 2) Добавить нового клиента (Сгенерировать ключ)"
    echo -e " 3) Показать всех клиентов"
    echo -e " 4) Удалить VPN сервер"
    echo -e " 0) Выход"
    echo -e ""
    read -r -p "Выберите действие [0-4]: " choice

    case $choice in
        1) install_server ;;
        2) add_client ;;
        3) list_clients ;;
        4) uninstall_server ;;
        0) echo -e "${GREEN}До свидания!${NC}"; exit 0 ;;
        *) echo -e "${RED}Неверный ввод!${NC}"; sleep 1 ;;
    esac
done
