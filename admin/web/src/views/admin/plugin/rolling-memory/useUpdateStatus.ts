import { onActivated, onBeforeUnmount, onDeactivated, onMounted, ref } from 'vue'
import { ElNotification } from 'element-plus'
import { useI18n } from 'vue-i18n'
import { useAdminInfo } from '@/stores/adminInfo'
import { MonitorSocket } from '@/utils/monitorSocket'
import request from '@/utils/request'

export interface UpdateState {
    ok?: boolean
    checking?: boolean
    stale?: boolean
    current_version?: string
    latest_tag?: string
    latest_release?: string
    latest_commit?: string
    latest_commit_short?: string
    installed_commit?: string
    channel?: string
    update_available?: boolean
    downgrade_available?: boolean
    assets_available?: boolean
    assets_checked?: boolean
    installable?: boolean
    needs_source_baseline?: boolean
    install_method?: string
    source_update?: string
    source?: string
    checked_at?: string
    error?: string
    [key: string]: any
}

const NOTIFIED_KEY = 'rm.update.notifiedTag'
const DISMISSED_KEY = 'rm.update.dismissedTag'
const BASELINE_KEY = 'rm.update.baselineNotified'
const AUTO_REFRESH_MS = 10 * 60 * 1000

/**
 * 版本更新实时状态：订阅 rm/update WebSocket 房间，缓存变化由后端主动推帧。
 * 前端不做高频轮询；打开控制台 / 进入仪表盘时强制刷新一次，之后每 10 分钟
 * 发一次轻量状态请求（后端仍按 10 分钟缓存周期执行检查），实时通道正常时
 * 检查结果仍由推送帧承载。
 */
export function useUpdateStatus(options: { onOpen?: () => void; autoRefresh?: boolean } = {}) {
    const { t } = useI18n()
    const adminInfo = useAdminInfo()
    const state = ref<UpdateState | null>(null)
    const connected = ref(false)
    let socket: MonitorSocket | null = null
    let fallbackInFlight = false
    let lastFallbackAt = 0
    let refreshTimer: ReturnType<typeof setInterval> | null = null

    const notify = (payload: UpdateState | null) => {
        if (!payload?.update_available || !payload.latest_tag) return
        const tag =
            payload.channel === 'dev' && payload.latest_commit_short ? `main@${payload.latest_commit_short}` : `v${String(payload.latest_tag)}`
        if (localStorage.getItem(DISMISSED_KEY) === tag) return
        if (localStorage.getItem(NOTIFIED_KEY) === tag) return
        localStorage.setItem(NOTIFIED_KEY, tag)
        const title = t('plugin.rolling-memory.systemUpdate.notifyTitle', { tag })
        const message =
            payload.assets_checked === true && payload.installable === false
                ? t('plugin.rolling-memory.systemUpdate.notifySourceOnly', { tag })
                : t('plugin.rolling-memory.systemUpdate.notifyMessage', { tag })
        const handle: any = ElNotification({
            title,
            message,
            duration: 9000,
            position: 'bottom-right',
            onClick: () => {
                handle?.close?.()
                options.onOpen?.()
            },
        })
        handle?.onClose?.(() => {
            localStorage.setItem(DISMISSED_KEY, tag)
        })
    }

    const notifyBaseline = (payload: UpdateState | null) => {
        if (!payload?.needs_source_baseline || payload.channel !== 'dev') return
        if (localStorage.getItem(BASELINE_KEY) === '1') return
        localStorage.setItem(BASELINE_KEY, '1')
        const handle: any = ElNotification({
            title: t('plugin.rolling-memory.systemUpdate.baselineTitle'),
            message: payload.source_update || t('plugin.rolling-memory.systemUpdate.baselineHint'),
            duration: 12000,
            position: 'bottom-right',
            onClick: () => {
                handle?.close?.()
                options.onOpen?.()
            },
        })
    }

    const apply = (payload: UpdateState | null) => {
        if (!payload) return
        state.value = payload
        notify(payload)
        notifyBaseline(payload)
    }

    const fetchOnce = async (force = false) => {
        if (fallbackInFlight) return
        const now = Date.now()
        if (!force && now - lastFallbackAt < 5 * 60 * 1000) return
        lastFallbackAt = now
        fallbackInFlight = true
        try {
            const res = await request<any>({
                url: '/admin/plugin/rolling-memory/system/update/status',
                method: 'POST',
                data: force ? { force: true } : {},
                timeout: 8000,
            })
            const payload = res?.data?.data ?? res?.data
            if (payload?.state) {
                apply(payload.state as UpdateState)
            }
        } catch {
            // 实时通道不可用时保持静默，用户仍可打开更新弹窗手动检查。
        } finally {
            fallbackInFlight = false
        }
    }

    const start = () => {
        if (socket) return
        const token = adminInfo.token
        if (!token) return
        socket = new MonitorSocket({
            token,
            room: 'rm/update',
            onStatus: (status) => {
                connected.value = status === 'connected'
                if (status === 'disabled' || status === 'reconnecting') {
                    void fetchOnce()
                }
            },
            onResync: () => {
                void fetchOnce()
            },
            onMessage: (env: any) => {
                if (env?.event === 'update' && env.data) {
                    connected.value = true
                    apply(env.data as UpdateState)
                }
            },
        })
        socket.start()
        if (options.autoRefresh && !refreshTimer) {
            refreshTimer = setInterval(() => {
                void fetchOnce(true)
            }, AUTO_REFRESH_MS)
        }
    }

    const stop = () => {
        if (socket) {
            socket.stop()
            socket = null
        }
        if (refreshTimer) {
            clearInterval(refreshTimer)
            refreshTimer = null
        }
        connected.value = false
    }

    onMounted(start)
    onActivated(start)
    onDeactivated(stop)
    onBeforeUnmount(stop)

    return { state, connected, refresh: (force = true) => fetchOnce(force) }
}
