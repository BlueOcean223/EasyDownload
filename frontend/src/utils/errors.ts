export function getErrorMessage(error: unknown, fallback = '未知错误'): string {
  if (error == null) return fallback

  if (typeof error === 'string') {
    const trimmed = error.trim()
    return trimmed || fallback
  }

  if (error instanceof Error) {
    const message = (error.message || '').trim()
    return message || fallback
  }

  if (typeof error === 'object') {
    const anyError = error as { message?: unknown; error?: unknown }
    if (typeof anyError.message === 'string' && anyError.message.trim()) {
      return anyError.message.trim()
    }
    if (typeof anyError.error === 'string' && anyError.error.trim()) {
      return anyError.error.trim()
    }
    try {
      const json = JSON.stringify(error)
      return json === '{}' ? fallback : json
    } catch {
      // ignore
    }
  }

  try {
    return String(error)
  } catch {
    return fallback
  }
}

export function isPermissionError(errorMsg: string): boolean {
  const msg = (errorMsg || '').trim()
  if (!msg) return false
  const lowerMsg = msg.toLowerCase()
  return (
    msg.includes('管理员') ||
    msg.includes('权限') ||
    msg.includes('拒绝访问') ||
    lowerMsg.includes('access is denied') ||
    lowerMsg.includes('elevation') ||
    lowerMsg.includes('privilege')
  )
}

