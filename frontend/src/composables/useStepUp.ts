/**
 * Step-up (sudo) 2FA composable.
 *
 * Wraps a sensitive admin action so that when the backend responds with a
 * STEP_UP_REQUIRED error, the caller can prompt for a TOTP code, obtain a
 * one-time grant, and transparently retry the original action exactly once.
 *
 * Usage in a view:
 *   const stepUp = useStepUp()
 *   async function exportData() {
 *     await stepUp.run(() => adminAPI.accounts.exportData(...))
 *   }
 *   // template: <TotpStepUpDialog :controller="stepUp" />
 */
import { ref } from 'vue'

/** Error codes the backend uses to signal step-up state. */
const STEP_UP_REQUIRED = 'STEP_UP_REQUIRED'
const STEP_UP_TOTP_NOT_ENABLED = 'STEP_UP_TOTP_NOT_ENABLED'
const STEP_UP_ADMIN_API_KEY_FORBIDDEN = 'STEP_UP_ADMIN_API_KEY_FORBIDDEN'
const STEP_UP_MACHINE_CREDENTIAL_FORBIDDEN = 'STEP_UP_MACHINE_CREDENTIAL_FORBIDDEN'
const STEP_UP_SESSION_REQUIRED = 'STEP_UP_SESSION_REQUIRED'

export const StepUpScope = {
  AccountsExport: 'accounts.export',
  ProxiesExport: 'proxies.export',
  BackupCreate: 'backups.create',
  BackupDownload: 'backups.download',
  BackupRestore: 'backups.restore',
  BackupDelete: 'backups.delete',
  BackupS3Read: 'backups.s3.read',
  BackupS3Update: 'backups.s3.update',
  AdminRolePromote: 'admin.role.promote',
  AuditClear: 'audit_logs.clear'
} as const

/**
 * Thrown by run() when the user dismisses the TOTP dialog.
 * Callers should treat it as a silent no-op, not an error to toast.
 */
export class StepUpCancelledError extends Error {
  readonly code = 'STEP_UP_CANCELLED'
  constructor() {
    super('step-up verification cancelled by user')
    this.name = 'StepUpCancelledError'
  }
}

export function isStepUpCancelled(err: unknown): boolean {
  return err instanceof StepUpCancelledError
}

interface ApiError {
  status?: number
  code?: string | number
  reason?: string
  message?: string
}

/** Extract the semantic error marker from either envelope shape (code or reason). */
function markerOf(err: unknown): string {
  const e = (err ?? {}) as ApiError
  const candidates = [e.code, e.reason].map((v) => (typeof v === 'string' ? v : ''))
  return candidates.find((v) => v.startsWith('STEP_UP')) || ''
}

export function isStepUpRequired(err: unknown): boolean {
  return markerOf(err) === STEP_UP_REQUIRED
}

export function isStepUpBlocked(err: unknown): boolean {
  const m = markerOf(err)
  return m === STEP_UP_TOTP_NOT_ENABLED ||
    m === STEP_UP_ADMIN_API_KEY_FORBIDDEN ||
    m === STEP_UP_MACHINE_CREDENTIAL_FORBIDDEN ||
    m === STEP_UP_SESSION_REQUIRED
}

export function stepUpBlockReason(err: unknown): string {
  return markerOf(err)
}

export type StepUpController = ReturnType<typeof useStepUp>

export function useStepUp() {
  const visible = ref(false)
  const blockedReason = ref<string>('')
  const actionScope = ref('')
  let resolver: ((token: string | null) => void) | null = null

  /** Open the TOTP dialog and resolve with a one-time grant token. */
  function prompt(scope: string): Promise<string | null> {
    actionScope.value = scope
    visible.value = true
    return new Promise<string | null>((resolve) => {
      resolver = resolve
    })
  }

  function onVerified(token: string) {
    visible.value = false
    resolver?.(token)
    resolver = null
    actionScope.value = ''
  }

  function onCancel() {
    visible.value = false
    resolver?.(null)
    resolver = null
    actionScope.value = ''
  }

  /**
   * Run a sensitive action. On STEP_UP_REQUIRED, prompt for a TOTP code and
   * retry once. STEP_UP_TOTP_NOT_ENABLED / admin-api-key errors are surfaced
   * to the caller (they cannot be resolved by entering a code). If the user
   * cancels the prompt, a StepUpCancelledError is thrown so callers can
   * distinguish "user changed their mind" from real failures.
   */
  async function run<T>(scope: string, action: (stepUpToken?: string) => Promise<T>): Promise<T> {
    try {
      return await action()
    } catch (err) {
      if (isStepUpBlocked(err)) {
        blockedReason.value = markerOf(err)
        throw err
      }
      if (!isStepUpRequired(err)) {
        throw err
      }
      const token = await prompt(scope)
      if (!token) {
        throw new StepUpCancelledError()
      }
      // 授权只在这次重放中通过 Header 发送；无全局缓存，刷新页面和并发请求都不会自动复用。
      return await action(token)
    }
  }

  return {
    visible,
    blockedReason,
    actionScope,
    prompt,
    onVerified,
    onCancel,
    run
  }
}
