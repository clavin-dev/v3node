#!/bin/bash
set -o pipefail

red='\033[0;31m'
green='\033[0;32m'
yellow='\033[0;33m'
plain='\033[0m'

cur_dir=$(pwd)
REPO_OWNER="${REPO_OWNER:-clavin-dev}"
REPO_NAME="${REPO_NAME:-v3node}"
REPO_BRANCH="${REPO_BRANCH:-main}"
GITHUB_API_BASE="https://api.github.com/repos/${REPO_OWNER}/${REPO_NAME}"
GITHUB_RAW_BASE="https://raw.githubusercontent.com/${REPO_OWNER}/${REPO_NAME}/${REPO_BRANCH}"
GITHUB_RELEASE_BASE="https://github.com/${REPO_OWNER}/${REPO_NAME}/releases/download"
MANAGER_CMD="${MANAGER_CMD:-v3node}"

# check root
[[ $EUID -ne 0 ]] && echo -e "${red}错误：${plain} 必须使用root用户运行此脚本！\n" && exit 1

# check os
if [[ -f /etc/redhat-release ]]; then
    release="centos"
elif cat /etc/issue | grep -Eqi "alpine"; then
    release="alpine"
elif cat /etc/issue | grep -Eqi "debian"; then
    release="debian"
elif cat /etc/issue | grep -Eqi "ubuntu"; then
    release="ubuntu"
elif cat /etc/issue | grep -Eqi "centos|red hat|redhat|rocky|alma|oracle linux"; then
    release="centos"
elif cat /proc/version | grep -Eqi "debian"; then
    release="debian"
elif cat /proc/version | grep -Eqi "ubuntu"; then
    release="ubuntu"
elif cat /proc/version | grep -Eqi "centos|red hat|redhat|rocky|alma|oracle linux"; then
    release="centos"
elif cat /proc/version | grep -Eqi "arch"; then
    release="arch"
else
    echo -e "${red}未检测到系统版本，请联系脚本作者！${plain}\n" && exit 1
fi

########################
# 参数解析
########################
VERSION_ARG=""
API_HOST_ARG=""
NODE_ID_ARG=""
API_KEY_ARG=""

parse_args() {
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --api-host)
                API_HOST_ARG="$2"; shift 2 ;;
            --node-id)
                NODE_ID_ARG="$2"; shift 2 ;;
            --api-key)
                API_KEY_ARG="$2"; shift 2 ;;
            -h|--help)
                echo "用法: $0 [版本号] [--api-host URL] [--node-id ID] [--api-key KEY]"
                exit 0 ;;
            --*)
                echo "未知参数: $1"; exit 1 ;;
            *)
                # 兼容第一个位置参数作为版本号
                if [[ -z "$VERSION_ARG" ]]; then
                    VERSION_ARG="$1"; shift
                else
                    shift
                fi ;;
        esac
    done
}

arch=$(uname -m)

if [[ $arch == "x86_64" || $arch == "x64" || $arch == "amd64" ]]; then
    arch="64"
elif [[ $arch == "aarch64" || $arch == "arm64" ]]; then
    arch="arm64-v8a"
elif [[ $arch == "s390x" ]]; then
    arch="s390x"
else
    arch="64"
    echo -e "${red}检测架构失败，使用默认架构: ${arch}${plain}"
fi

if [ "$(getconf WORD_BIT)" != '32' ] && [ "$(getconf LONG_BIT)" != '64' ] ; then
    echo "本软件不支持 32 位系统(x86)，请使用 64 位系统(x86_64)，如果检测有误，请联系作者"
    exit 2
fi

# os version
if [[ -f /etc/os-release ]]; then
    os_version=$(awk -F'[= ."]' '/VERSION_ID/{print $3}' /etc/os-release)
fi
if [[ -z "$os_version" && -f /etc/lsb-release ]]; then
    os_version=$(awk -F'[= ."]+' '/DISTRIB_RELEASE/{print $2}' /etc/lsb-release)
fi

if [[ x"${release}" == x"centos" ]]; then
    if [[ ${os_version} -le 6 ]]; then
        echo -e "${red}请使用 CentOS 7 或更高版本的系统！${plain}\n" && exit 1
    fi
    if [[ ${os_version} -eq 7 ]]; then
        echo -e "${red}注意： CentOS 7 无法使用hysteria1/2协议！${plain}\n"
    fi
elif [[ x"${release}" == x"ubuntu" ]]; then
    if [[ ${os_version} -lt 16 ]]; then
        echo -e "${red}请使用 Ubuntu 16 或更高版本的系统！${plain}\n" && exit 1
    fi
elif [[ x"${release}" == x"debian" ]]; then
    if [[ ${os_version} -lt 8 ]]; then
        echo -e "${red}请使用 Debian 8 或更高版本的系统！${plain}\n" && exit 1
    fi
fi

install_base() {
    # 优化版本：批量检查和安装包，减少系统调用
    need_install_apt() {
        local packages=("$@")
        local missing=()
        
        # 批量检查已安装的包
        local installed_list=$(dpkg-query -W -f='${Package}\n' 2>/dev/null | sort)
        
        for p in "${packages[@]}"; do
            if ! echo "$installed_list" | grep -q "^${p}$"; then
                missing+=("$p")
            fi
        done
        
        if [[ ${#missing[@]} -gt 0 ]]; then
            echo "安装缺失的包: ${missing[*]}"
            apt-get update -y >/dev/null 2>&1
            DEBIAN_FRONTEND=noninteractive apt-get install -y "${missing[@]}" >/dev/null 2>&1
        fi
    }

    need_install_yum() {
        local packages=("$@")
        local missing=()
        
        # 批量检查已安装的包
        local installed_list=$(rpm -qa --qf '%{NAME}\n' 2>/dev/null | sort)
        
        for p in "${packages[@]}"; do
            if ! echo "$installed_list" | grep -q "^${p}$"; then
                missing+=("$p")
            fi
        done
        
        if [[ ${#missing[@]} -gt 0 ]]; then
            echo "安装缺失的包: ${missing[*]}"
            yum install -y "${missing[@]}" >/dev/null 2>&1
        fi
    }

    need_install_apk() {
        local packages=("$@")
        local missing=()
        
        # 批量检查已安装的包
        local installed_list=$(apk info 2>/dev/null | sort)
        
        for p in "${packages[@]}"; do
            if ! echo "$installed_list" | grep -q "^${p}$"; then
                missing+=("$p")
            fi
        done
        
        if [[ ${#missing[@]} -gt 0 ]]; then
            echo "安装缺失的包: ${missing[*]}"
            apk add --no-cache "${missing[@]}" >/dev/null 2>&1
        fi
    }

    # 一次性安装所有必需的包
    if [[ x"${release}" == x"centos" ]]; then
        # 检查并安装 epel-release
        if ! rpm -q epel-release >/dev/null 2>&1; then
            echo "安装 EPEL 源..."
            yum install -y epel-release >/dev/null 2>&1
        fi
        need_install_yum wget curl unzip tar cronie socat ca-certificates pv
        update-ca-trust force-enable >/dev/null 2>&1 || true
    elif [[ x"${release}" == x"alpine" ]]; then
        need_install_apk wget curl unzip tar socat ca-certificates pv
        update-ca-certificates >/dev/null 2>&1 || true
    elif [[ x"${release}" == x"debian" ]]; then
        need_install_apt wget curl unzip tar cron socat ca-certificates pv
        update-ca-certificates >/dev/null 2>&1 || true
    elif [[ x"${release}" == x"ubuntu" ]]; then
        need_install_apt wget curl unzip tar cron socat ca-certificates pv
        update-ca-certificates >/dev/null 2>&1 || true
    elif [[ x"${release}" == x"arch" ]]; then
        echo "更新包数据库..."
        pacman -Sy --noconfirm >/dev/null 2>&1
        # --needed 会跳过已安装的包，非常高效
        echo "安装必需的包..."
        pacman -S --noconfirm --needed wget curl unzip tar cronie socat ca-certificates pv >/dev/null 2>&1
    fi
}

# 0: running, 1: not running, 2: not installed
check_status() {
    if [[ ! -f /usr/local/v3node/v3node ]]; then
        return 2
    fi
    if [[ x"${release}" == x"alpine" ]]; then
        temp=$(service v3node status | awk '{print $3}')
        if [[ x"${temp}" == x"started" ]]; then
            return 0
        else
            return 1
        fi
    else
        temp=$(systemctl status v3node | grep Active | awk '{print $3}' | cut -d "(" -f2 | cut -d ")" -f1)
        if [[ x"${temp}" == x"running" ]]; then
            return 0
        else
            return 1
        fi
    fi
}

generate_v3node_config() {
        local api_host="$1"
        local node_id="$2"
        local api_key="$3"

        mkdir -p /etc/v3node >/dev/null 2>&1
        cat > /etc/v3node/config.json <<EOF
{
    "Log": {
        "Level": "warning",
        "Output": "",
        "Access": "none"
    },
    "Nodes": [
        {
            "ApiHost": "${api_host}",
            "NodeID": ${node_id},
            "ApiKey": "${api_key}",
            "Timeout": 15,
            "PanelOfflineMode": true,
            "PanelOfflineFailThreshold": 3
        }
    ]
}
EOF
        echo -e "${green}V3node 配置文件生成完成,正在重新启动服务${plain}"
        if [[ x"${release}" == x"alpine" ]]; then
            service v3node restart
        else
            systemctl restart v3node
        fi
        sleep 2
        check_status
        echo -e ""
        if [[ $? == 0 ]]; then
            echo -e "${green}v3node 重启成功${plain}"
        else
            echo -e "${red}v3node 可能启动失败，请使用 v3node log 查看日志信息${plain}"
        fi
}

download_release_zip() {
    local version="$1"
    local target="/usr/local/v3node/v3node-linux.zip"
    local candidates=()
    local asset url

    candidates+=("v3node-linux-${arch}.zip")
    case "$arch" in
        arm64-v8a)
            candidates+=("v3node-linux-arm64.zip")
            ;;
        64)
            candidates+=("v3node-linux-amd64.zip")
            ;;
    esac
    candidates+=("v3node-linux.zip")

    for asset in "${candidates[@]}"; do
        url="${GITHUB_RELEASE_BASE}/${version}/${asset}"
        echo -e "${yellow}尝试下载附件: ${asset}${plain}"
        if curl -fL "$url" | pv -W -N "下载进度" > "$target"; then
            if unzip -tqq "$target" >/dev/null 2>&1; then
                echo -e "${green}下载成功: ${asset}${plain}"
                return 0
            fi
            echo -e "${yellow}附件校验失败: ${asset}${plain}"
        fi
        rm -f "$target"
    done

    return 1
}

install_v3node() {
    local version_param="$1"

    # 从旧服务名迁移，避免同机存在两个服务实例
    if [[ x"${release}" == x"alpine" ]]; then
        if [[ -f /etc/init.d/v2node ]]; then
            service v2node stop >/dev/null 2>&1 || true
            rc-update del v2node >/dev/null 2>&1 || true
            rm -f /etc/init.d/v2node
        fi
    else
        if systemctl list-unit-files 2>/dev/null | grep -q '^v2node\.service'; then
            systemctl stop v2node >/dev/null 2>&1 || true
            systemctl disable v2node >/dev/null 2>&1 || true
            rm -f /etc/systemd/system/v2node.service
            systemctl daemon-reload >/dev/null 2>&1 || true
        fi
    fi

    if [[ -e /usr/local/v3node/ ]]; then
        rm -rf /usr/local/v3node/
    fi
    if [[ -e /usr/local/v2node/ ]]; then
        rm -rf /usr/local/v2node/
    fi

    mkdir /usr/local/v3node/ -p
    cd /usr/local/v3node/

    if  [[ -z "$version_param" ]] ; then
        last_version=$(curl -Ls "${GITHUB_API_BASE}/releases/latest" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')
        if [[ ! -n "$last_version" ]]; then
            echo -e "${red}检测 v3node 版本失败，可能是超出 Github API 限制，请稍后再试，或手动指定 v3node 版本安装${plain}"
            exit 1
        fi
        echo -e "${green}检测到最新版本：${last_version}，开始安装...${plain}"
        if ! download_release_zip "$last_version"; then
            echo -e "${red}下载 v3node 失败(版本: ${last_version}, 架构: ${arch})，请确认 Release 附件是否包含该架构${plain}"
            exit 1
        fi
    else
        last_version=$version_param
        if ! download_release_zip "$last_version"; then
            echo -e "${red}下载 v3node ${last_version} 失败(架构: ${arch})，请确保此版本存在且包含对应架构附件${plain}"
            exit 1
        fi
    fi

    if ! unzip -o v3node-linux.zip; then
        echo -e "${red}解压失败，下载文件可能损坏${plain}"
        exit 1
    fi
    rm v3node-linux.zip -f
    if [[ ! -f v3node || ! -f geoip.dat || ! -f geosite.dat ]]; then
        echo -e "${red}安装包内容不完整，请检查 Release 附件是否正确${plain}"
        exit 1
    fi
    chmod +x v3node
    mkdir /etc/v3node/ -p
    cp geoip.dat /etc/v3node/
    cp geosite.dat /etc/v3node/

    # 迁移旧配置目录
    if [[ -f /etc/v2node/config.json && ! -f /etc/v3node/config.json ]]; then
        cp /etc/v2node/config.json /etc/v3node/config.json
    fi
    if [[ x"${release}" == x"alpine" ]]; then
        rm /etc/init.d/v3node -f
        cat <<EOF > /etc/init.d/v3node
#!/sbin/openrc-run

name="v3node"
description="v3node"

command="/usr/local/v3node/v3node"
command_args="server"
command_user="root"

pidfile="/run/v3node.pid"
command_background="yes"

depend() {
        need net
}
EOF
        chmod +x /etc/init.d/v3node
        rc-update add v3node default
        echo -e "${green}v3node ${last_version}${plain} 安装完成，已设置开机自启"
    else
        rm /etc/systemd/system/v3node.service -f
        cat <<EOF > /etc/systemd/system/v3node.service
[Unit]
Description=v3node Service
After=network.target nss-lookup.target
Wants=network.target

[Service]
User=root
Group=root
Type=simple
LimitAS=infinity
LimitRSS=infinity
LimitCORE=infinity
LimitNOFILE=999999
WorkingDirectory=/usr/local/v3node/
ExecStart=/usr/local/v3node/v3node server
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF
        systemctl daemon-reload
        systemctl stop v3node
        systemctl enable v3node
        echo -e "${green}v3node ${last_version}${plain} 安装完成，已设置开机自启"
    fi

    if [[ ! -f /etc/v3node/config.json ]]; then
        # 如果通过 CLI 传入了完整参数，则直接生成配置并跳过交互
        if [[ -n "$API_HOST_ARG" && -n "$NODE_ID_ARG" && -n "$API_KEY_ARG" ]]; then
            generate_v3node_config "$API_HOST_ARG" "$NODE_ID_ARG" "$API_KEY_ARG"
            echo -e "${green}已根据参数生成 /etc/v3node/config.json${plain}"
            first_install=false
        else
            if [[ -f config.json ]]; then
                cp config.json /etc/v3node/
            fi
            first_install=true
        fi
    else
        if [[ x"${release}" == x"alpine" ]]; then
            service v3node start
        else
            systemctl start v3node
        fi
        sleep 2
        check_status
        echo -e ""
        if [[ $? == 0 ]]; then
            echo -e "${green}v3node 重启成功${plain}"
        else
            echo -e "${red}v3node 可能启动失败，请使用 v3node log 查看日志信息${plain}"
        fi
        first_install=false
    fi


    curl -o "/usr/bin/${MANAGER_CMD}" -Ls "${GITHUB_RAW_BASE}/script/v3node.sh"
    chmod +x "/usr/bin/${MANAGER_CMD}"
    rm -f /usr/bin/v2node

    cd $cur_dir
    rm -f install.sh
    echo "------------------------------------------"
    echo -e "管理脚本使用方法(主命令: ${MANAGER_CMD}): "
    echo "------------------------------------------"
    echo "${MANAGER_CMD}              - 显示管理菜单 (功能更多)"
    echo "${MANAGER_CMD} start        - 启动 v3node"
    echo "${MANAGER_CMD} stop         - 停止 v3node"
    echo "${MANAGER_CMD} restart      - 重启 v3node"
    echo "${MANAGER_CMD} status       - 查看 v3node 状态"
    echo "${MANAGER_CMD} enable       - 设置 v3node 开机自启"
    echo "${MANAGER_CMD} disable      - 取消 v3node 开机自启"
    echo "${MANAGER_CMD} log          - 查看 v3node 日志"
    echo "${MANAGER_CMD} generate     - 生成 v3node 配置文件"
    echo "${MANAGER_CMD} update       - 更新 v3node"
    echo "${MANAGER_CMD} update x.x.x - 更新 v3node 指定版本"
    echo "${MANAGER_CMD} install      - 安装 v3node"
    echo "${MANAGER_CMD} uninstall    - 卸载 v3node"
    echo "${MANAGER_CMD} version      - 查看 v3node 版本"
    echo "------------------------------------------"
    curl -fsS --max-time 10 "https://api.v-50.me/counter" || true

    if [[ $first_install == true ]]; then
        read -rp "检测到你为第一次安装 v3node，是否自动生成 /etc/v3node/config.json？(y/n): " if_generate
        if [[ "$if_generate" =~ ^[Yy]$ ]]; then
            # 交互式收集参数，提供示例默认值
            read -rp "面板API地址[格式: https://example.com/]: " api_host
            api_host=${api_host:-https://example.com/}
            read -rp "节点ID: " node_id
            node_id=${node_id:-1}
            read -rp "节点通讯密钥: " api_key

            # 生成配置文件（覆盖可能从包中复制的模板）
            generate_v3node_config "$api_host" "$node_id" "$api_key"
        else
            echo "${green}已跳过自动生成配置。如需后续生成，可执行: ${MANAGER_CMD} generate${plain}"
        fi
    fi
}

parse_args "$@"
echo -e "${green}开始安装${plain}"
install_base
install_v3node "$VERSION_ARG"
