// @vitest-environment node
import { describe, expect, it } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import { resolve } from 'node:path'

const frontendRoot = resolve(process.cwd())
const repositoryRoot = resolve(frontendRoot, '..')

function read(path: string) {
  return readFileSync(path, 'utf8')
}

function readSourceTree(path: string): string {
  return readdirSync(path, { withFileTypes: true })
    .flatMap(entry => {
      const entryPath = resolve(path, entry.name)
      if (entry.isDirectory()) return [readSourceTree(entryPath)]
      if (/\.(ts|vue)$/.test(entry.name)) return [read(entryPath)]
      return []
    })
    .join('\n')
}

function generatedClassBlock(models: string, name: string) {
  const start = models.indexOf(`export class ${name} {`)
  expect(start, `${name} must be generated`).toBeGreaterThanOrEqual(0)
  const next = models.indexOf('\n\texport class ', start + 1)
  return models.slice(start, next === -1 ? models.length : next)
}

function generatedClassFields(models: string, name: string) {
  return Object.fromEntries(
    [...generatedClassBlock(models, name).matchAll(/^\s+([A-Za-z]\w*)(\?)?:\s*([^;]+);/gm)]
      .map(match => [match[1], {
        optional: match[2] === '?',
        type: match[3].trim().replace(/\s+/g, ' ')
      }])
  )
}

describe('settings Wails boundary', () => {
  it('does not reintroduce removed field-level bindings or frontend calls', () => {
    const denylist = read(resolve(frontendRoot, 'settings-bindings.denylist.txt'))
      .split(/\r?\n/)
      .map(line => line.trim())
      .filter(line => line && !line.startsWith('#'))
    const appSource = read(resolve(repositoryRoot, 'app.go'))
    const generatedDeclarations = read(resolve(frontendRoot, 'wailsjs/go/main/App.d.ts'))
    const frontendSources = readSourceTree(resolve(frontendRoot, 'src'))

    for (const removedName of denylist) {
      expect(appSource, `${removedName} must not be an exported App method`).not.toMatch(
        new RegExp(`func\\s+\\(\\s*\\w+\\s+\\*App\\s*\\)\\s+${removedName}\\s*\\(`)
      )
      expect(generatedDeclarations, `${removedName} must not be a generated binding`).not.toMatch(
        new RegExp(`export\\s+function\\s+${removedName}\\s*\\(`)
      )
      expect(frontendSources, `${removedName} must not be called by the frontend`).not.toMatch(
        new RegExp(`\\b${removedName}\\b`)
      )
    }
  })

  it('does not bypass the generated settings contract with unsafe assertions', () => {
    const store = read(resolve(frontendRoot, 'src/stores/app.ts'))
    expect(store).not.toMatch(/\bas\s+any\b/)
    expect(store).not.toMatch(/\bas\s+unknown\s+as\b/)
    for (const binding of [
      'GetSettings', 'UpdateSettings', 'GetDetectedVideos', 'ClearDetectedVideos',
      'StartDetectedDownload', 'GetDownloads', 'TakeLegacyDownloadNotice',
      'PauseDownload', 'CancelDownload', 'RemoveDownload'
    ]) {
      expect(store, `${binding} results must be normalized without a type assertion`).not.toMatch(
        new RegExp(`${binding}\\([^)]*\\)\\s+as\\b`)
      )
    }
  })

  it('locks generated settings field types and optionality', () => {
    const models = read(resolve(frontendRoot, 'wailsjs/go/models.ts'))
    const declarations = read(resolve(frontendRoot, 'wailsjs/go/main/App.d.ts'))
    const required = (type: string) => ({ optional: false, type })
    const optional = (type: string) => ({ optional: true, type })

    expect(generatedClassFields(models, 'SettingsSnapshot')).toEqual({
      proxyPort: required('number'),
      apiPort: required('number'),
      downloadDir: required('string'),
      maxConcurrent: required('number'),
      minimizeToTray: required('boolean'),
      showNotification: required('boolean'),
      firstRunComplete: required('boolean'),
      closeAction: required('string'),
      dontAskOnClose: required('boolean'),
      theme: required('string'),
      language: required('string'),
      upstreamProxy: required('string'),
      useUpstreamProxy: required('boolean'),
      proxyDebug: required('boolean'),
      dontRemindCertWizard: required('boolean')
    })
    expect(generatedClassFields(models, 'SettingsPatch')).toEqual({
      proxyPort: optional('number'),
      apiPort: optional('number'),
      downloadDir: optional('string'),
      maxConcurrent: optional('number'),
      minimizeToTray: optional('boolean'),
      showNotification: optional('boolean'),
      firstRunComplete: optional('boolean'),
      closeAction: optional('string'),
      dontAskOnClose: optional('boolean'),
      theme: optional('string'),
      language: optional('string'),
      upstreamProxy: optional('string'),
      useUpstreamProxy: optional('boolean'),
      proxyDebug: optional('boolean'),
      dontRemindCertWizard: optional('boolean')
    })
    expect(generatedClassFields(models, 'SettingsWarning')).toEqual({
      code: required('string'),
      effect: optional('string'),
      message: required('string')
    })
    expect(generatedClassFields(models, 'RestartRequirement')).toEqual({
      scope: required('string'),
      fields: required('string[]'),
      reason: required('string')
    })
    expect(generatedClassFields(models, 'SettingsUpdateResult')).toEqual({
      settings: required('SettingsSnapshot'),
      warnings: optional('SettingsWarning[]'),
      restartRequired: required('boolean'),
      restartRequirements: optional('RestartRequirement[]')
    })
    expect(declarations).toContain('GetSettings():Promise<settings.SettingsSnapshot>')
    expect(declarations).toContain('UpdateSettings(arg1:settings.SettingsPatch):Promise<settings.SettingsUpdateResult>')
  })

  it('uses runtime API metadata for internal proxy URLs', () => {
    for (const component of ['ProxiedImage.vue', 'ImagePreviewModal.vue']) {
      const source = read(resolve(frontendRoot, 'src/components', component))
      expect(source, `${component} must use the currently bound API port`).toContain('store.appInfo?.apiPort')
      expect(source, `${component} must not switch ports before restart`).not.toContain('store.settings?.apiPort')
    }
  })

  it('locks generated detection and download projections to reviewed public fields', () => {
    const models = read(resolve(frontendRoot, 'wailsjs/go/models.ts'))
    const declarations = read(resolve(frontendRoot, 'wailsjs/go/main/App.d.ts'))
    const classFields = (name: string) => {
      return Object.keys(generatedClassFields(models, name)).sort()
    }
    expect(classFields('ResourceDTO')).toEqual([
      'default', 'durationMs', 'encrypted', 'fileFormat', 'height', 'id',
      'label', 'mimeType', 'quality', 'sizeBytes', 'width'
    ].sort())
    expect(classFields('VideoDTO')).toEqual([
      'author', 'authorAvatar', 'candidates', 'coverUrl', 'detectedAt', 'durationMs',
      'height', 'id', 'isCurrent', 'lastSeenAt', 'pageUrl', 'platform', 'source',
      'title', 'width'
    ].sort())
    expect(classFields('PublicTaskArtifact')).toEqual([
      'cleanupFailed', 'createdAt', 'fileName', 'id', 'kind', 'mediaType',
      'path', 'primary', 'size'
    ].sort())
    expect(classFields('PublicDownloadTask')).toEqual([
      'artifacts', 'completedAt', 'cover', 'createdAt', 'displaySource', 'error',
      'executionState', 'generation', 'id', 'instance', 'lastError',
      'lastErrorDetail', 'outputPolicy', 'platformId', 'progressSummary', 'revision',
      'speed', 'status', 'title'
    ].sort())
    expect(declarations).toContain('PauseDownload(arg1:string,arg2:number,arg3:number):Promise<downloader.StopReceipt>')
    expect(declarations).toContain('StartDetectedDownload(arg1:string,arg2:string):Promise<downloader.PublicDownloadTask>')
  })
})
