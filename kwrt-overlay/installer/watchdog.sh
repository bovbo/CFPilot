#!/bin/sh
# CFPilot 资源看门狗：定时采样内存 / 连接跟踪表，异常时只重启 cfpilot（不动路由器其它服务）。
#
# 可调参数写在 /etc/cfpilot/watchdog.conf（改完 /etc/init.d/cfpilot restart 生效）：
#   RSS_MAX_MB=80      cfpilot 进程占用超过该值（连续 3 次）→ 重启
#   MIN_FREE_MB=14     系统可用内存低于该值（连续 3 次）→ 重启 cfpilot 腾内存
#   CT_PCT=88          连接跟踪表占用超过该百分比（连续 2 次）→ 重启 cfpilot（它在大量并发连接）
#   INTERVAL=30        检查间隔（秒）
#   SAMPLE_EVERY=10    每 N 次检查写一行采样（默认 30s × 10 = 5 分钟）
#   COOLDOWN=600       两次自动重启之间至少间隔（秒），防止反复重启
RSS_MAX_MB=80
MIN_FREE_MB=14
CT_PCT=88
INTERVAL=30
SAMPLE_EVERY=10
COOLDOWN=600
MEM_LOG=/tmp/cfpilot-mem.log
MEM_LOG_MAX=300
[ -f /etc/cfpilot/watchdog.conf ] && . /etc/cfpilot/watchdog.conf

log() { logger -t cfpilot-watchdog "$*"; }
now_str() { date '+%Y-%m-%d %H:%M:%S'; }

cf_pid() { pidof cfpilot 2>/dev/null | awk '{print $1}'; }

rss_mb() {
    p="$1"
    [ -r "/proc/$p/status" ] || return 1
    awk '/^VmRSS:/{printf "%d", $2/1024}' "/proc/$p/status"
}

avail_mb() {
    awk '/^MemAvailable:/{a=$2} /^MemFree:/{f=$2} /^Buffers:/{b=$2} /^Cached:/{c=$2}
         END{ if (a=="") a=f+b+c; printf "%d", a/1024 }' /proc/meminfo
}

ct_pct() {
    c=$(cat /proc/sys/net/netfilter/nf_conntrack_count 2>/dev/null)
    m=$(cat /proc/sys/net/netfilter/nf_conntrack_max 2>/dev/null)
    if [ -n "$c" ] && [ -n "$m" ] && [ "$m" -gt 0 ] 2>/dev/null; then
        echo $((c * 100 / m))
    else
        echo 0
    fi
}

write_sample() {
    echo "$(now_str) $1" >> "$MEM_LOG"
    n=$(wc -l < "$MEM_LOG" 2>/dev/null || echo 0)
    if [ "$n" -gt "$MEM_LOG_MAX" ]; then
        tail -n $((MEM_LOG_MAX / 2)) "$MEM_LOG" > "$MEM_LOG.tmp" && mv "$MEM_LOG.tmp" "$MEM_LOG"
    fi
}

# 只重启 cfpilot 本身：TERM 后由 procd 按 respawn 规则拉起；10 秒还没退再 KILL
restart_cfpilot() {
    p="$1"
    kill -TERM "$p" 2>/dev/null
    i=0
    while [ $i -lt 10 ] && kill -0 "$p" 2>/dev/null; do sleep 1; i=$((i + 1)); done
    kill -0 "$p" 2>/dev/null && kill -KILL "$p" 2>/dev/null
}

bad_rss=0; bad_mem=0; bad_ct=0; tick=0; last_restart=0
log "看门狗已启动：RSS≤${RSS_MAX_MB}MB，可用内存≥${MIN_FREE_MB}MB，连接表≤${CT_PCT}%，间隔 ${INTERVAL}s"

while true; do
    sleep "$INTERVAL"
    tick=$((tick + 1))
    pid=$(cf_pid)
    if [ -z "$pid" ]; then
        bad_rss=0; bad_mem=0; bad_ct=0
        continue
    fi
    rss=$(rss_mb "$pid"); [ -z "$rss" ] && continue
    avail=$(avail_mb)
    ct=$(ct_pct)
    load=$(cut -d' ' -f1 /proc/loadavg 2>/dev/null)

    if [ "$rss" -gt "$RSS_MAX_MB" ]; then bad_rss=$((bad_rss + 1)); else bad_rss=0; fi
    if [ "$avail" -lt "$MIN_FREE_MB" ]; then bad_mem=$((bad_mem + 1)); else bad_mem=0; fi
    if [ "$ct" -ge "$CT_PCT" ]; then bad_ct=$((bad_ct + 1)); else bad_ct=0; fi

    # 采样：定期写一行；异常期间每次都写，方便事后看是什么把路由器压住的
    if [ $((tick % SAMPLE_EVERY)) -eq 1 ] || [ $bad_rss -gt 0 ] || [ $bad_mem -gt 0 ] || [ $bad_ct -gt 0 ]; then
        write_sample "rss=${rss}MB avail=${avail}MB conntrack=${ct}% load=${load}"
    fi

    reason=""
    [ $bad_rss -ge 3 ] && reason="cfpilot 占用 ${rss}MB，超过 ${RSS_MAX_MB}MB"
    [ -z "$reason" ] && [ $bad_mem -ge 3 ] && reason="系统可用内存仅剩 ${avail}MB，低于 ${MIN_FREE_MB}MB"
    [ -z "$reason" ] && [ $bad_ct -ge 2 ] && reason="连接跟踪表已用 ${ct}%（大量并发连接），超过 ${CT_PCT}%"

    if [ -n "$reason" ]; then
        now=$(date +%s)
        if [ $((now - last_restart)) -ge "$COOLDOWN" ]; then
            log "$reason，重启 cfpilot"
            write_sample "RESTART $reason"
            restart_cfpilot "$pid"
            last_restart=$now
            bad_rss=0; bad_mem=0; bad_ct=0
        elif [ $((tick % 10)) -eq 0 ]; then
            log "$reason（距上次自动重启不足 ${COOLDOWN}s，本次不重复重启）"
        fi
    fi
done
