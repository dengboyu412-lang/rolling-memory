<template>
    <el-dialog
        v-model="visible"
        :title="t('plugin.rolling-memory.systemUpdate.dialogTitle')"
        width="min(820px, 95vw)"
        destroy-on-close
        append-to-body
    >
        <div v-loading="loadingVersion" :element-loading-text="versionLoadingText" class="rm-sys-update" data-testid="dialog-system-update">
            <el-alert
                type="info"
                :closable="false"
                show-icon
                class="su-alert"
                :title="t('plugin.rolling-memory.systemUpdate.safetyNotice')"
            />

            <!-- 版本概览栏 -->
            <div class="su-ver-strip">
                <div class="su-ver-box">
                    <span class="su-ver-lbl">{{ t('plugin.rolling-memory.systemUpdate.currentVerLbl') }}</span>
                    <div class="su-ver-val">
                        <strong>v{{ localInfo.current_version || '—' }}</strong>
                        <el-tag size="small" type="primary" effect="plain">
                            {{ localInfo.os || 'darwin' }}/{{ localInfo.arch || 'arm64' }}
                        </el-tag>
                    </div>
                </div>
                <div class="su-ver-arrow">→</div>
                <div class="su-ver-box">
                    <span class="su-ver-lbl">{{ t('plugin.rolling-memory.systemUpdate.latestVerLbl') }}</span>
                    <div class="su-ver-val">
                        <template v-if="checkResult">
                            <strong>{{ latestDisplay }}</strong>
                            <el-tag
                                v-if="checkResult.update_available"
                                size="small"
                                type="warning"
                                effect="dark"
                                data-testid="tag-update-available"
                            >
                                {{ t('plugin.rolling-memory.systemUpdate.updateAvailTag') }}
                            </el-tag>
                            <el-tag
                                v-else-if="checkResult.needs_source_baseline"
                                size="small"
                                type="info"
                                effect="dark"
                                data-testid="tag-dev-baseline"
                            >
                                {{ t('plugin.rolling-memory.systemUpdate.baselineTag') }}
                            </el-tag>
                            <el-tag v-else size="small" type="success" effect="dark" data-testid="tag-up-to-date">
                                {{ t('plugin.rolling-memory.systemUpdate.upToDateTag') }}
                            </el-tag>
                        </template>
                        <span v-else class="su-ver-muted">{{ t('plugin.rolling-memory.systemUpdate.notCheckedYet') }}</span>
                    </div>
                </div>
            </div>

            <el-alert
                v-if="checkResult && checkResult.update_available && checkResult.assets_checked === true && checkResult.installable === false"
                type="warning"
                :closable="false"
                show-icon
                class="su-alert"
                data-testid="alert-source-only-update"
                :title="t('plugin.rolling-memory.systemUpdate.sourceOnlyTitle')"
                :description="checkResult.source_update || (checkResult.channel === 'dev' ? t('plugin.rolling-memory.systemUpdate.sourceOnlyHintDev') : t('plugin.rolling-memory.systemUpdate.sourceOnlyHint', { tag: latestDisplay }))"
            />
            <el-alert
                v-if="checkResult?.asset_fallback"
                type="success"
                :closable="false"
                show-icon
                class="su-alert"
                data-testid="alert-asset-fallback"
                :title="t('plugin.rolling-memory.systemUpdate.fallbackTitle')"
                :description="t('plugin.rolling-memory.systemUpdate.fallbackHint')"
            />
            <el-button
                v-if="checkResult?.channel === 'dev'"
                size="small"
                type="primary"
                plain
                data-testid="btn-switch-stable"
                @click="switchToStable"
            >
                {{ t('plugin.rolling-memory.systemUpdate.switchToStable') }}
            </el-button>

            <el-alert
                v-if="checkResult?.channel === 'dev' && checkResult?.needs_source_baseline"
                type="info"
                :closable="false"
                show-icon
                class="su-alert"
                data-testid="alert-dev-baseline"
                :title="t('plugin.rolling-memory.systemUpdate.baselineTitle')"
                :description="checkResult.source_update || t('plugin.rolling-memory.systemUpdate.baselineHint')"
            />

            <!-- 更新源 -->
            <div class="su-channel-bar">
                <span class="su-channel-lbl">{{ t('plugin.rolling-memory.systemUpdate.updateSourceLbl') }}</span>
                <el-select
                    v-model="form.source"
                    size="small"
                    style="width: 220px"
                    data-testid="select-update-source"
                    @change="handleSourceChange"
                >
                    <el-option :label="t('plugin.rolling-memory.systemUpdate.sourceGithub')" value="github" />
                    <el-option :label="t('plugin.rolling-memory.systemUpdate.sourceGitee')" value="gitee" />
                    <el-option :label="t('plugin.rolling-memory.systemUpdate.sourceCustom')" value="custom" />
                </el-select>
                <span class="su-ver-muted">{{ sourceHint }}</span>
            </div>

            <!-- 更新通道 -->
            <div class="su-channel-bar">
                <span class="su-channel-lbl">{{ t('plugin.rolling-memory.systemUpdate.updateChannelLbl') }}</span>
                <el-select
                    v-model="form.channel"
                    size="small"
                    style="width: 220px"
                    data-testid="select-update-channel"
                    @change="handleChannelChange"
                >
                    <el-option :label="t('plugin.rolling-memory.systemUpdate.channelStable')" value="stable" />
                    <el-option :label="t('plugin.rolling-memory.systemUpdate.channelBeta')" value="beta" />
                    <el-option :label="t('plugin.rolling-memory.systemUpdate.channelDev')" value="dev" />
                </el-select>
                <span class="su-ver-muted">{{ channelHint }}</span>
            </div>

            <!-- 后台更新策略 -->
            <div class="su-channel-bar">
                <span class="su-channel-lbl">{{ t('plugin.rolling-memory.systemUpdate.updateAutoLbl') }}</span>
                <el-select
                    v-model="form.auto"
                    size="small"
                    style="width: 220px"
                    data-testid="select-update-auto"
                    @change="handleAutoChange"
                >
                    <el-option :label="t('plugin.rolling-memory.systemUpdate.autoOff')" value="off" />
                    <el-option :label="t('plugin.rolling-memory.systemUpdate.autoNotify')" value="notify" />
                    <el-option
                        :label="t('plugin.rolling-memory.systemUpdate.autoApply')"
                        value="apply"
                        :disabled="form.channel === 'dev'"
                    />
                </el-select>
                <span class="su-ver-muted">{{ autoHint }}</span>
            </div>

            <!-- 发布渠道 / 镜像源 URL -->
            <div class="su-channel-bar">
                <span class="su-channel-lbl">{{ t('plugin.rolling-memory.systemUpdate.channelLbl') }}</span>
                <el-input
                    v-model="form.release_base"
                    size="small"
                    clearable
                    :disabled="form.source !== 'custom'"
                    :placeholder="t('plugin.rolling-memory.systemUpdate.channelPlaceholder')"
                    data-testid="input-release-base"
                    @input="handleReleaseBaseInput"
                    @keyup.enter="handleCheckUpdate"
                />
            </div>

            <!-- 三大组件 SHA-256 指纹对账表 -->
            <el-table :data="componentRows" border size="small" class="su-table">
                <el-table-column prop="label" :label="t('plugin.rolling-memory.systemUpdate.compCol')" min-width="150" />
                <el-table-column prop="asset" :label="t('plugin.rolling-memory.systemUpdate.assetCol')" min-width="210">
                    <template #default="{ row }">
                        <div class="su-mono">{{ row.asset }}</div>
                        <div v-if="row.path" class="su-sub-path">{{ row.path }}</div>
                    </template>
                </el-table-column>
                <el-table-column :label="t('plugin.rolling-memory.systemUpdate.localShaCol')" width="145">
                    <template #default="{ row }">
                        <code class="su-sha">{{ shortSha(row.localSha) }}</code>
                    </template>
                </el-table-column>
                <el-table-column :label="t('plugin.rolling-memory.systemUpdate.remoteShaCol')" width="145">
                    <template #default="{ row }">
                        <code class="su-sha">{{ row.remoteSha ? shortSha(row.remoteSha) : '—' }}</code>
                    </template>
                </el-table-column>
                <el-table-column :label="t('plugin.rolling-memory.systemUpdate.statusCol')" width="130" align="center">
                    <template #default="{ row }">
                        <el-tag v-if="row.needsUpdate" size="small" type="warning">
                            {{ t('plugin.rolling-memory.systemUpdate.statusNeedsUpdate') }}
                        </el-tag>
                        <el-tag v-else-if="row.remoteSha" size="small" type="success">
                            {{ t('plugin.rolling-memory.systemUpdate.statusUpToDate') }}
                        </el-tag>
                        <el-tag v-else size="small" type="info" effect="plain">
                            {{ t('plugin.rolling-memory.systemUpdate.statusLocalOnly') }}
                        </el-tag>
                    </template>
                </el-table-column>
            </el-table>

            <!-- 升级选项 -->
            <div class="su-options">
                <el-checkbox v-model="form.with_console">
                    {{ t('plugin.rolling-memory.systemUpdate.withConsoleOpt') }}
                </el-checkbox>
                <el-checkbox v-model="form.refresh_harness">
                    {{ t('plugin.rolling-memory.systemUpdate.refreshHarnessOpt') }}
                </el-checkbox>
                <el-checkbox v-model="form.force">
                    {{ t('plugin.rolling-memory.systemUpdate.forceOpt') }}
                </el-checkbox>
            </div>

            <!-- 操作按钮行 -->
            <div class="su-actions">
                <el-button
                    type="default"
                    :loading="checking"
                    :disabled="applying"
                    data-testid="btn-update-check"
                    @click="handleCheckUpdate"
                >
                    {{ t('plugin.rolling-memory.systemUpdate.checkBtn') }}
                    <template v-if="checking"> ({{ checkPct }}%)</template>
                </el-button>
                <el-button
                    type="primary"
                    :loading="applying"
                    :disabled="checking || form.channel === 'dev' || Boolean(checkResult?.update_available && checkResult?.assets_checked === true && checkResult?.installable === false)"
                    data-testid="btn-update-apply"
                    @click="handleApplyUpdate"
                >
                    {{ t('plugin.rolling-memory.systemUpdate.applyBtn') }}
                    <template v-if="applying"> ({{ applyPct }}%)</template>
                </el-button>
            </div>

            <!-- 命令行等价参考 -->
            <div class="su-cli-box">
                <div class="su-cli-title">{{ t('plugin.rolling-memory.systemUpdate.cliHintTitle') }}</div>
                <pre class="su-cli-pre">rolling-memory update --check          # check SHA256SUMS without modifying files
rolling-memory update                  # update CLI, console (if installed), and host integrations
rolling-memory update --with-console   # also install/update local web console (:9006 + :8008)</pre>
            </div>
        </div>
    </el-dialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ElMessage } from 'element-plus'
import { useLoadingProgressText } from '@/utils/loadingProgress'
import request from '@/utils/request'

const props = defineProps<{
    modelValue: boolean
}>()
const emit = defineEmits<{
    (e: 'update:modelValue', v: boolean): void
    (e: 'updated'): void
}>()

const { t } = useI18n()

const visible = computed({
    get: () => props.modelValue,
    set: (v: boolean) => emit('update:modelValue', v),
})

const loadingVersion = ref(false)
const checking = ref(false)
const applying = ref(false)
const { text: versionLoadingText } = useLoadingProgressText(loadingVersion)
const { percent: checkPct } = useLoadingProgressText(checking, undefined, 1200)
const { percent: applyPct } = useLoadingProgressText(applying, undefined, 2500)

const localInfo = ref<any>({
    current_version: '',
    os: 'darwin',
    arch: 'arm64',
    release_base: '',
    update_auto: 'notify',
    cli: {},
    console: {},
})

const checkResult = ref<any>(null)

const GITHUB_RELEASE_BASE = 'https://github.com/rocareer/rolling-memory/releases/latest/download'
const GITEE_RELEASE_BASE = 'https://gitee.com/rocareer/Rolling-Memory/releases/latest/download'

const sourceForBase = (base?: string) => {
    const value = (base || '').trim().toLowerCase()
    if (!value) return 'github'
    if (value === GITHUB_RELEASE_BASE.toLowerCase()) return 'github'
    if (value === GITEE_RELEASE_BASE.toLowerCase()) return 'gitee'
    return 'custom'
}

const form = ref({
    release_base: '',
    channel: 'stable',
    source: 'github',
    auto: 'notify',
    with_console: true,
    refresh_harness: true,
    force: false,
})

const latestDisplay = computed(() => {
    const result = checkResult.value
    if (result?.channel === 'dev') {
        return result.latest_commit_short ? `main@${result.latest_commit_short}` : 'main'
    }
    if (result?.latest_tag) {
        return `v${result.latest_tag}`
    }
    return `v${localInfo.value.current_version || '—'}`
})

const channelHint = computed(() => {
    switch (form.value.channel) {
        case 'beta':
            return t('plugin.rolling-memory.systemUpdate.channelHintBeta')
        case 'dev':
            return t('plugin.rolling-memory.systemUpdate.channelHintDev')
        default:
            return t('plugin.rolling-memory.systemUpdate.channelHintStable')
    }
})

const autoHint = computed(() => {
    if (form.value.channel === 'dev') {
        return t('plugin.rolling-memory.systemUpdate.autoHintDev')
    }
    switch (form.value.auto) {
        case 'off':
            return t('plugin.rolling-memory.systemUpdate.autoHintOff')
        case 'apply':
            return t('plugin.rolling-memory.systemUpdate.autoHintApply')
        default:
            return t('plugin.rolling-memory.systemUpdate.autoHintNotify')
    }
})

const sourceHint = computed(() => {
    switch (form.value.source) {
        case 'gitee':
            return t('plugin.rolling-memory.systemUpdate.sourceHintGitee')
        case 'custom':
            return t('plugin.rolling-memory.systemUpdate.sourceHintCustom')
        default:
            return t('plugin.rolling-memory.systemUpdate.sourceHintGithub')
    }
})

const shortSha = (sha?: string) => {
    if (!sha) return 'none'
    return sha.length > 12 ? sha.slice(0, 12) : sha
}

const componentRows = computed(() => {
    const os = localInfo.value.os || 'darwin'
    const arch = localInfo.value.arch || 'arm64'
    const ext = os === 'windows' ? '.exe' : ''
    const cliCheck = checkResult.value?.cli || {}
    const conCheck = checkResult.value?.console || {}

    return [
        {
            label: t('plugin.rolling-memory.systemUpdate.compCli'),
            asset: cliCheck.asset || `rolling-memory-${os}-${arch}${ext}`,
            path: cliCheck.path || localInfo.value.cli?.path || '',
            localSha: cliCheck.local_sha256 || localInfo.value.cli?.local_sha256 || '',
            remoteSha: cliCheck.remote_sha256 || '',
            needsUpdate: Boolean(cliCheck.update_available),
        },
        {
            label: t('plugin.rolling-memory.systemUpdate.compAdmin'),
            asset: conCheck.admin_asset || `rolling-admin-${os}-${arch}${ext}`,
            path: localInfo.value.console?.admin_path || '',
            localSha: conCheck.admin_local_sha || localInfo.value.console?.admin_local_sha || '',
            remoteSha: conCheck.admin_remote_sha || '',
            needsUpdate: Boolean(conCheck.admin_update),
        },
        {
            label: t('plugin.rolling-memory.systemUpdate.compWeb'),
            asset: 'web-dist.tar.gz',
            path: localInfo.value.console?.web_dist_path || '',
            localSha: conCheck.web_local_sha || localInfo.value.console?.web_local_sha || '',
            remoteSha: conCheck.web_remote_sha || '',
            needsUpdate: Boolean(conCheck.web_update),
        },
    ]
})

const loadLocalVersion = async () => {
    loadingVersion.value = true
    try {
        const res = await request<any>({
            url: '/admin/plugin/rolling-memory/system/version',
            method: 'POST',
            data: {},
        })
        const payload = res?.data?.data ?? (res?.data as any)
        if (payload) {
            localInfo.value = payload
            const baseSource = sourceForBase(payload.release_base)
            form.value.source = baseSource === 'custom' ? 'custom' : payload.update_source || baseSource
            if (!form.value.release_base && payload.release_base) {
                form.value.release_base = payload.release_base
            }
            const channel = payload.update_channel || payload.update_state?.channel
            if (channel) {
                form.value.channel = channel
            }
            if (payload.update_auto) {
                form.value.auto = payload.update_auto
            }
            // 首帧直接展示后台缓存，避免打开弹窗先显示“未检查”。
            if (!checkResult.value && payload.update_state?.latest_tag && payload.update_state?.ok !== false) {
                checkResult.value = payload.update_state
            }
        }
    } catch {
        // Keep defaults on error
    } finally {
        loadingVersion.value = false
    }
}

const handleSourceChange = async () => {
    if (form.value.source === 'custom') {
        checkResult.value = null
        return
    }
    const releaseBase = form.value.source === 'gitee' ? GITEE_RELEASE_BASE : GITHUB_RELEASE_BASE
    form.value.release_base = releaseBase
    try {
        const res = await request<any>({
            url: '/admin/plugin/rolling-memory/system/update/source',
            method: 'POST',
            data: { source: form.value.source },
        })
        const payload = res?.data?.data ?? (res?.data as any)
        if (payload?.release_base) {
            form.value.release_base = payload.release_base
        }
        checkResult.value = null
        await handleCheckUpdate()
    } catch {
        // Error message already displayed by request interceptor.
    }
}

const handleReleaseBaseInput = () => {
    form.value.source = 'custom'
    checkResult.value = null
}

const handleAutoChange = async () => {
    try {
        const res = await request<any>({
            url: '/admin/plugin/rolling-memory/system/update/auto',
            method: 'POST',
            data: { auto: form.value.auto },
        })
        const payload = res?.data?.data ?? (res?.data as any)
        if (payload?.schedule_error) {
            ElMessage.warning(
                t('plugin.rolling-memory.systemUpdate.autoScheduleFailed', { error: payload.schedule_error }),
            )
        } else {
            ElMessage.success(t('plugin.rolling-memory.systemUpdate.autoSaved'))
        }
        await loadLocalVersion()
    } catch {
        // Error message already displayed by request interceptor.
    }
}

const switchToStable = async () => {
    form.value.channel = 'stable'
    await handleChannelChange()
}

const handleChannelChange = async () => {
    try {
        await request({
            url: '/admin/plugin/rolling-memory/system/update/channel',
            method: 'POST',
            data: { channel: form.value.channel },
        })
        if (form.value.channel === 'dev' && form.value.auto === 'apply') {
            form.value.auto = 'notify'
            await handleAutoChange()
        }
        checkResult.value = null
        await handleCheckUpdate()
    } catch {
        // Error message already displayed by request interceptor.
    }
}

const handleCheckUpdate = async () => {
    checking.value = true
    try {
        const res = await request<any>({
            url: '/admin/plugin/rolling-memory/system/update/check',
            method: 'POST',
            timeout: 30000,
            data: {
                release_base: form.value.release_base.trim(),
                channel: form.value.channel,
                source: form.value.source === 'custom' ? '' : form.value.source,
            },
        })
        const payload = res?.data?.data ?? (res?.data as any)
        if (payload) {
            checkResult.value = payload
            if (payload.channel) {
                form.value.channel = payload.channel
            }
            if (payload.needs_source_baseline) {
                ElMessage.info(t('plugin.rolling-memory.systemUpdate.baselineTitle'))
            } else if (payload.update_available) {
                if (payload.assets_checked === true && payload.installable === false) {
                    ElMessage.warning(t('plugin.rolling-memory.systemUpdate.checkSuccessSourceOnly'))
                } else {
                    ElMessage.warning(t('plugin.rolling-memory.systemUpdate.checkSuccessAvail'))
                }
            } else {
                ElMessage.success(t('plugin.rolling-memory.systemUpdate.checkSuccessUpToDate'))
            }
        }
    } catch {
        // Error message already displayed by request interceptor
    } finally {
        checking.value = false
    }
}

// The updater replaces the console backend and then restarts it from a
// detached helper. Poll until the backend has gone away and come back so the
// page reloads against the newly built frontend instead of the old bundle.
const waitForConsoleRestart = async () => {
    const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms))
    let sawDown = false
    for (let attempt = 0; attempt < 90; attempt += 1) {
        try {
            const response = await fetch('/admin/login-config', { cache: 'no-store' })
            if (response.ok) {
                if (sawDown) {
                    window.location.reload()
                    return
                }
            } else {
                sawDown = true
            }
        } catch {
            sawDown = true
        }
        if (!sawDown && attempt >= 20) {
            // The helper may have completed before the first probe; reload
            // rather than leaving the user on a stale page.
            window.location.reload()
            return
        }
        await sleep(1000)
    }
    window.location.reload()
}

const handleApplyUpdate = async () => {
    if (form.value.channel === 'dev') {
        ElMessage.warning(checkResult.value?.source_update || t('plugin.rolling-memory.systemUpdate.channelHintDev'))
        return
    }
    if (checkResult.value?.update_available && checkResult.value?.assets_checked === true && checkResult.value?.installable === false) {
        ElMessage.warning(checkResult.value.source_update || (checkResult.value.channel === 'dev' ? t('plugin.rolling-memory.systemUpdate.sourceOnlyHintDev') : t('plugin.rolling-memory.systemUpdate.sourceOnlyHint', { tag: latestDisplay.value })))
        return
    }
    applying.value = true
    try {
        const res = await request<any>({
            url: '/admin/plugin/rolling-memory/system/update/apply',
            method: 'POST',
            timeout: 185000,
            data: {
                release_base: form.value.release_base.trim(),
                channel: form.value.channel,
                source: form.value.source === 'custom' ? '' : form.value.source,
                with_console: form.value.with_console,
                no_install: !form.value.refresh_harness,
                force: form.value.force,
            },
        })
        const data: any = res?.data?.data ?? res?.data ?? {}
        const comps: string[] = Array.isArray(data.components) ? data.components : []
        if (data.updated && comps.length > 0) {
            ElMessage.success(t('plugin.rolling-memory.systemUpdate.applySuccessUpdated', { components: comps.join(', ') }))
        } else {
            ElMessage.info(t('plugin.rolling-memory.systemUpdate.applySuccessNoop'))
        }
        await loadLocalVersion()
        emit('updated')
        if (data.console_restart_deferred) {
            ElMessage.info(t('plugin.rolling-memory.systemUpdate.restartingNotice'))
            await waitForConsoleRestart()
        }
    } catch {
        // Error message already displayed by request interceptor
    } finally {
        applying.value = false
    }
}

watch(
    () => props.modelValue,
    (open) => {
        if (open) {
            checkResult.value = null
            loadLocalVersion()
        }
    }
)
</script>

<style scoped>
.rm-sys-update {
    display: flex;
    flex-direction: column;
    gap: 14px;
}
.su-ver-strip {
    display: flex;
    align-items: center;
    gap: 14px;
    padding: 12px 16px;
    background: var(--el-fill-color-light);
    border-radius: 8px;
}
.su-ver-box {
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: 4px;
}
.su-ver-lbl {
    font-size: 12px;
    color: var(--el-text-color-secondary);
}
.su-ver-val {
    display: flex;
    align-items: center;
    gap: 8px;
    font-size: 15px;
}
.su-ver-muted {
    font-size: 13px;
    color: var(--el-text-color-secondary);
}
.su-ver-arrow {
    font-size: 18px;
    color: var(--el-text-color-placeholder);
}
.su-channel-bar {
    display: flex;
    align-items: center;
    gap: 10px;
}
.su-channel-lbl {
    font-size: 13px;
    white-space: nowrap;
    color: var(--el-text-color-regular);
}
.su-mono {
    font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
    font-size: 12px;
    font-weight: 600;
}
.su-sub-path {
    font-size: 11px;
    color: var(--el-text-color-secondary);
    word-break: break-all;
}
.su-sha {
    font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
    font-size: 12px;
    padding: 2px 6px;
    background: var(--el-fill-color);
    border-radius: 4px;
}
.su-options {
    display: flex;
    flex-direction: column;
    gap: 4px;
}
.su-actions {
    display: flex;
    justify-content: flex-end;
    gap: 10px;
}
.su-cli-box {
    padding: 10px 12px;
    background: var(--el-fill-color-lighter);
    border-radius: 6px;
    border: 1px solid var(--el-border-color-lighter);
}
.su-cli-title {
    font-size: 12px;
    font-weight: 600;
    margin-bottom: 6px;
    color: var(--el-text-color-regular);
}
.su-cli-pre {
    margin: 0;
    font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace;
    font-size: 12px;
    line-height: 1.5;
    white-space: pre-wrap;
    color: var(--el-text-color-primary);
}
</style>
