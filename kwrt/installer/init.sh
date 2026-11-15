#!/bin/sh /etc/rc.common
# CFPilot 路由器版：procd 守护（崩溃自动重启）+ 资源看门狗
START=99
STOP=10
USE_PROCD=1

start_service() {
    mkdir -p /etc/cfpilot
    rm -f /tmp/cfpilot.url

    procd_open_instance main
    procd_set_param command /usr/bin/cfpilot --data-dir /etc/cfpilot --url-file /tmp/cfpilot.url --log-max-kb 512 --log-backups 1
    # 软内存上限 64MiB；单核运行，把另一个核留给路由转发；低优先级，转发 / DNS / DHCP 优先
    procd_set_param env GOMEMLIMIT=64MiB GOGC=50 GOMAXPROCS=1
    procd_set_param nice 10
    procd_set_param limits nofile="2048 2048"
    procd_set_param respawn 3600 5 0
    procd_set_param stdout 1
    procd_set_param stderr 1
    procd_close_instance

    procd_open_instance watchdog
    procd_set_param command /usr/bin/cfpilot-watchdog
    procd_set_param respawn 3600 10 0
    procd_set_param stdout 1
    procd_set_param stderr 1
    procd_close_instance
}
