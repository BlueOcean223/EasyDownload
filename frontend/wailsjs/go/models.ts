export namespace bilibili {
	
	export class BilibiliLoginStatus {
	    code: number;
	    message: string;
	
	    static createFrom(source: any = {}) {
	        return new BilibiliLoginStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.code = source["code"];
	        this.message = source["message"];
	    }
	}
	export class BilibiliStream {
	    quality: number;
	    qualityName: string;
	    format: string;
	    size: number;
	    videoUrl: string;
	    audioUrl: string;
	    width: number;
	    height: number;
	    frameRate: string;
	    codecs: string;
	    codecId: number;
	    mimeType: string;
	    backupUrls: string[];
	    audioBackupUrls: string[];
	    drmKey?: string;
	    drmTechType?: number;
	    kid?: string;
	    biliDrmUri?: string;
	
	    static createFrom(source: any = {}) {
	        return new BilibiliStream(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.quality = source["quality"];
	        this.qualityName = source["qualityName"];
	        this.format = source["format"];
	        this.size = source["size"];
	        this.videoUrl = source["videoUrl"];
	        this.audioUrl = source["audioUrl"];
	        this.width = source["width"];
	        this.height = source["height"];
	        this.frameRate = source["frameRate"];
	        this.codecs = source["codecs"];
	        this.codecId = source["codecId"];
	        this.mimeType = source["mimeType"];
	        this.backupUrls = source["backupUrls"];
	        this.audioBackupUrls = source["audioBackupUrls"];
	        this.drmKey = source["drmKey"];
	        this.drmTechType = source["drmTechType"];
	        this.kid = source["kid"];
	        this.biliDrmUri = source["biliDrmUri"];
	    }
	}
	export class BilibiliPart {
	    cid: number;
	    page: number;
	    partName: string;
	    duration: number;
	    streams?: BilibiliStream[];
	    bv?: string;
	    aid?: number;
	    epId?: number;
	    badge?: string;
	    badgeType?: number;
	    sectionType?: number;
	    cover?: string;
	
	    static createFrom(source: any = {}) {
	        return new BilibiliPart(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.cid = source["cid"];
	        this.page = source["page"];
	        this.partName = source["partName"];
	        this.duration = source["duration"];
	        this.streams = this.convertValues(source["streams"], BilibiliStream);
	        this.bv = source["bv"];
	        this.aid = source["aid"];
	        this.epId = source["epId"];
	        this.badge = source["badge"];
	        this.badgeType = source["badgeType"];
	        this.sectionType = source["sectionType"];
	        this.cover = source["cover"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class BilibiliQRCode {
	    url: string;
	    qrcodeKey: string;
	
	    static createFrom(source: any = {}) {
	        return new BilibiliQRCode(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.url = source["url"];
	        this.qrcodeKey = source["qrcodeKey"];
	    }
	}
	
	export class BilibiliUserInfo {
	    isLogin: boolean;
	    uid: number;
	    username: string;
	    face: string;
	    isVip: boolean;
	    vipType: number;
	    vipStatus: number;
	
	    static createFrom(source: any = {}) {
	        return new BilibiliUserInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.isLogin = source["isLogin"];
	        this.uid = source["uid"];
	        this.username = source["username"];
	        this.face = source["face"];
	        this.isVip = source["isVip"];
	        this.vipType = source["vipType"];
	        this.vipStatus = source["vipStatus"];
	    }
	}
	export class BilibiliVideo {
	    bv: string;
	    av: string;
	    title: string;
	    cover: string;
	    author: string;
	    duration: number;
	    desc: string;
	    parts: BilibiliPart[];
	    streams: BilibiliStream[];
	    seasonId?: number;
	    mediaId?: number;
	    epId?: number;
	    badge?: string;
	    seasonType?: number;
	    isBangumi: boolean;
	    totalEps?: number;
	    currentPartIndex: number;
	
	    static createFrom(source: any = {}) {
	        return new BilibiliVideo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.bv = source["bv"];
	        this.av = source["av"];
	        this.title = source["title"];
	        this.cover = source["cover"];
	        this.author = source["author"];
	        this.duration = source["duration"];
	        this.desc = source["desc"];
	        this.parts = this.convertValues(source["parts"], BilibiliPart);
	        this.streams = this.convertValues(source["streams"], BilibiliStream);
	        this.seasonId = source["seasonId"];
	        this.mediaId = source["mediaId"];
	        this.epId = source["epId"];
	        this.badge = source["badge"];
	        this.seasonType = source["seasonType"];
	        this.isBangumi = source["isBangumi"];
	        this.totalEps = source["totalEps"];
	        this.currentPartIndex = source["currentPartIndex"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace detection {
	
	export class ResourceDTO {
	    id: string;
	    label: string;
	    quality?: string;
	    fileFormat?: string;
	    mimeType?: string;
	    width?: number;
	    height?: number;
	    durationMs?: number;
	    sizeBytes?: number;
	    encrypted?: boolean;
	    default?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ResourceDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.label = source["label"];
	        this.quality = source["quality"];
	        this.fileFormat = source["fileFormat"];
	        this.mimeType = source["mimeType"];
	        this.width = source["width"];
	        this.height = source["height"];
	        this.durationMs = source["durationMs"];
	        this.sizeBytes = source["sizeBytes"];
	        this.encrypted = source["encrypted"];
	        this.default = source["default"];
	    }
	}
	export class VideoDTO {
	    id: string;
	    source: string;
	    platform: string;
	    title: string;
	    author?: string;
	    pageUrl?: string;
	    coverUrl?: string;
	    authorAvatar?: string;
	    durationMs?: number;
	    width?: number;
	    height?: number;
	    isCurrent?: boolean;
	    candidates: ResourceDTO[];
	    // Go type: time
	    detectedAt: any;
	    // Go type: time
	    lastSeenAt: any;
	
	    static createFrom(source: any = {}) {
	        return new VideoDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.source = source["source"];
	        this.platform = source["platform"];
	        this.title = source["title"];
	        this.author = source["author"];
	        this.pageUrl = source["pageUrl"];
	        this.coverUrl = source["coverUrl"];
	        this.authorAvatar = source["authorAvatar"];
	        this.durationMs = source["durationMs"];
	        this.width = source["width"];
	        this.height = source["height"];
	        this.isCurrent = source["isCurrent"];
	        this.candidates = this.convertValues(source["candidates"], ResourceDTO);
	        this.detectedAt = this.convertValues(source["detectedAt"], null);
	        this.lastSeenAt = this.convertValues(source["lastSeenAt"], null);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class PublicSnapshot {
	    revision: number;
	    videos: VideoDTO[];
	
	    static createFrom(source: any = {}) {
	        return new PublicSnapshot(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.revision = source["revision"];
	        this.videos = this.convertValues(source["videos"], VideoDTO);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class PublicChange {
	    type: string;
	    changedId?: string;
	    snapshot: PublicSnapshot;
	
	    static createFrom(source: any = {}) {
	        return new PublicChange(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.type = source["type"];
	        this.changedId = source["changedId"];
	        this.snapshot = this.convertValues(source["snapshot"], PublicSnapshot);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	

}

export namespace douyin {
	
	export class Image {
	    URL: string;
	    VideoURL: string;
	    Width: number;
	    Height: number;
	
	    static createFrom(source: any = {}) {
	        return new Image(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.URL = source["URL"];
	        this.VideoURL = source["VideoURL"];
	        this.Width = source["Width"];
	        this.Height = source["Height"];
	    }
	}
	export class Stream {
	    QualityKey: string;
	    QualityName: string;
	    Width: number;
	    Height: number;
	    Bitrate: number;
	    URL: string;
	    Size: number;
	
	    static createFrom(source: any = {}) {
	        return new Stream(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.QualityKey = source["QualityKey"];
	        this.QualityName = source["QualityName"];
	        this.Width = source["Width"];
	        this.Height = source["Height"];
	        this.Bitrate = source["Bitrate"];
	        this.URL = source["URL"];
	        this.Size = source["Size"];
	    }
	}
	export class DouyinItem {
	    Type: string;
	    ID: string;
	    Title: string;
	    Cover: string;
	    Author: string;
	    AuthorID: string;
	    Duration: number;
	    Streams: Stream[];
	    Images: Image[];
	
	    static createFrom(source: any = {}) {
	        return new DouyinItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Type = source["Type"];
	        this.ID = source["ID"];
	        this.Title = source["Title"];
	        this.Cover = source["Cover"];
	        this.Author = source["Author"];
	        this.AuthorID = source["AuthorID"];
	        this.Duration = source["Duration"];
	        this.Streams = this.convertValues(source["Streams"], Stream);
	        this.Images = this.convertValues(source["Images"], Image);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	

}

export namespace downloader {
	
	export class LegacyTaskStateNotice {
	    code: string;
	    legacyPath: string;
	    v2Path: string;
	    imported: boolean;
	    preserved: boolean;
	    rollbackAvailable: boolean;
	    message: string;
	
	    static createFrom(source: any = {}) {
	        return new LegacyTaskStateNotice(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.code = source["code"];
	        this.legacyPath = source["legacyPath"];
	        this.v2Path = source["v2Path"];
	        this.imported = source["imported"];
	        this.preserved = source["preserved"];
	        this.rollbackAvailable = source["rollbackAvailable"];
	        this.message = source["message"];
	    }
	}
	export class PublicTaskArtifact {
	    id?: string;
	    kind: string;
	    path: string;
	    fileName?: string;
	    mediaType?: string;
	    size?: number;
	    primary?: boolean;
	    createdAt?: number;
	    cleanupFailed?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new PublicTaskArtifact(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.kind = source["kind"];
	        this.path = source["path"];
	        this.fileName = source["fileName"];
	        this.mediaType = source["mediaType"];
	        this.size = source["size"];
	        this.primary = source["primary"];
	        this.createdAt = source["createdAt"];
	        this.cleanupFailed = source["cleanupFailed"];
	    }
	}
	export class PublicOutputPolicy {
	    directory: string;
	    plannedFilename: string;
	    plannedFinalPath: string;
	    conflictStrategy: string;
	
	    static createFrom(source: any = {}) {
	        return new PublicOutputPolicy(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.directory = source["directory"];
	        this.plannedFilename = source["plannedFilename"];
	        this.plannedFinalPath = source["plannedFinalPath"];
	        this.conflictStrategy = source["conflictStrategy"];
	    }
	}
	export class PublicDownloadTask {
	    id: string;
	    instance: number;
	    generation: number;
	    revision: number;
	    platformId?: string;
	    title: string;
	    cover: string;
	    displaySource?: string;
	    outputPolicy: PublicOutputPolicy;
	    progressSummary: task.TaskProgressSummary;
	    artifacts?: PublicTaskArtifact[];
	    speed: number;
	    status: string;
	    error: string;
	    createdAt: number;
	    completedAt: number;
	    lastError: string;
	    lastErrorDetail?: task.TaskError;
	    executionState?: string;
	
	    static createFrom(source: any = {}) {
	        return new PublicDownloadTask(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.instance = source["instance"];
	        this.generation = source["generation"];
	        this.revision = source["revision"];
	        this.platformId = source["platformId"];
	        this.title = source["title"];
	        this.cover = source["cover"];
	        this.displaySource = source["displaySource"];
	        this.outputPolicy = this.convertValues(source["outputPolicy"], PublicOutputPolicy);
	        this.progressSummary = this.convertValues(source["progressSummary"], task.TaskProgressSummary);
	        this.artifacts = this.convertValues(source["artifacts"], PublicTaskArtifact);
	        this.speed = source["speed"];
	        this.status = source["status"];
	        this.error = source["error"];
	        this.createdAt = source["createdAt"];
	        this.completedAt = source["completedAt"];
	        this.lastError = source["lastError"];
	        this.lastErrorDetail = this.convertValues(source["lastErrorDetail"], task.TaskError);
	        this.executionState = source["executionState"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	
	export class StopReceipt {
	    accepted: boolean;
	    operationId: string;
	    taskId: string;
	    requestedReason: string;
	    effectiveReason: string;
	    executionState: string;
	    revision: number;
	    taskInstance: number;
	    taskGeneration: number;
	    taskRevision: number;
	    error?: task.TaskError;
	
	    static createFrom(source: any = {}) {
	        return new StopReceipt(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.accepted = source["accepted"];
	        this.operationId = source["operationId"];
	        this.taskId = source["taskId"];
	        this.requestedReason = source["requestedReason"];
	        this.effectiveReason = source["effectiveReason"];
	        this.executionState = source["executionState"];
	        this.revision = source["revision"];
	        this.taskInstance = source["taskInstance"];
	        this.taskGeneration = source["taskGeneration"];
	        this.taskRevision = source["taskRevision"];
	        this.error = this.convertValues(source["error"], task.TaskError);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace main {
	
	export class AppRuntimeInfo {
	    version: string;
	    apiPort: number;
	    apiToken?: string;
	    ffmpegPath?: string;
	    certPath?: string;
	    certInstalled: boolean;
	    ffmpegAvailable: boolean;
	
	    static createFrom(source: any = {}) {
	        return new AppRuntimeInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	        this.apiPort = source["apiPort"];
	        this.apiToken = source["apiToken"];
	        this.ffmpegPath = source["ffmpegPath"];
	        this.certPath = source["certPath"];
	        this.certInstalled = source["certInstalled"];
	        this.ffmpegAvailable = source["ffmpegAvailable"];
	    }
	}

}

export namespace settings {
	
	export class RestartRequirement {
	    scope: string;
	    fields: string[];
	    reason: string;
	
	    static createFrom(source: any = {}) {
	        return new RestartRequirement(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.scope = source["scope"];
	        this.fields = source["fields"];
	        this.reason = source["reason"];
	    }
	}
	export class SettingsPatch {
	    proxyPort?: number;
	    apiPort?: number;
	    downloadDir?: string;
	    maxConcurrent?: number;
	    minimizeToTray?: boolean;
	    showNotification?: boolean;
	    firstRunComplete?: boolean;
	    closeAction?: string;
	    dontAskOnClose?: boolean;
	    theme?: string;
	    language?: string;
	    upstreamProxy?: string;
	    useUpstreamProxy?: boolean;
	    proxyDebug?: boolean;
	    dontRemindCertWizard?: boolean;
	
	    static createFrom(source: any = {}) {
	        return new SettingsPatch(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.proxyPort = source["proxyPort"];
	        this.apiPort = source["apiPort"];
	        this.downloadDir = source["downloadDir"];
	        this.maxConcurrent = source["maxConcurrent"];
	        this.minimizeToTray = source["minimizeToTray"];
	        this.showNotification = source["showNotification"];
	        this.firstRunComplete = source["firstRunComplete"];
	        this.closeAction = source["closeAction"];
	        this.dontAskOnClose = source["dontAskOnClose"];
	        this.theme = source["theme"];
	        this.language = source["language"];
	        this.upstreamProxy = source["upstreamProxy"];
	        this.useUpstreamProxy = source["useUpstreamProxy"];
	        this.proxyDebug = source["proxyDebug"];
	        this.dontRemindCertWizard = source["dontRemindCertWizard"];
	    }
	}
	export class SettingsSnapshot {
	    proxyPort: number;
	    apiPort: number;
	    downloadDir: string;
	    maxConcurrent: number;
	    minimizeToTray: boolean;
	    showNotification: boolean;
	    firstRunComplete: boolean;
	    closeAction: string;
	    dontAskOnClose: boolean;
	    theme: string;
	    language: string;
	    upstreamProxy: string;
	    useUpstreamProxy: boolean;
	    proxyDebug: boolean;
	    dontRemindCertWizard: boolean;
	
	    static createFrom(source: any = {}) {
	        return new SettingsSnapshot(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.proxyPort = source["proxyPort"];
	        this.apiPort = source["apiPort"];
	        this.downloadDir = source["downloadDir"];
	        this.maxConcurrent = source["maxConcurrent"];
	        this.minimizeToTray = source["minimizeToTray"];
	        this.showNotification = source["showNotification"];
	        this.firstRunComplete = source["firstRunComplete"];
	        this.closeAction = source["closeAction"];
	        this.dontAskOnClose = source["dontAskOnClose"];
	        this.theme = source["theme"];
	        this.language = source["language"];
	        this.upstreamProxy = source["upstreamProxy"];
	        this.useUpstreamProxy = source["useUpstreamProxy"];
	        this.proxyDebug = source["proxyDebug"];
	        this.dontRemindCertWizard = source["dontRemindCertWizard"];
	    }
	}
	export class SettingsWarning {
	    code: string;
	    effect?: string;
	    message: string;
	
	    static createFrom(source: any = {}) {
	        return new SettingsWarning(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.code = source["code"];
	        this.effect = source["effect"];
	        this.message = source["message"];
	    }
	}
	export class SettingsUpdateResult {
	    settings: SettingsSnapshot;
	    warnings?: SettingsWarning[];
	    restartRequired: boolean;
	    restartRequirements?: RestartRequirement[];
	
	    static createFrom(source: any = {}) {
	        return new SettingsUpdateResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.settings = this.convertValues(source["settings"], SettingsSnapshot);
	        this.warnings = this.convertValues(source["warnings"], SettingsWarning);
	        this.restartRequired = source["restartRequired"];
	        this.restartRequirements = this.convertValues(source["restartRequirements"], RestartRequirement);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

export namespace task {
	
	export class TaskError {
	    code: string;
	    category: string;
	    message: string;
	    retryable: boolean;
	    userAction?: string;
	    cause?: string;
	    metadata?: Record<string, string>;
	
	    static createFrom(source: any = {}) {
	        return new TaskError(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.code = source["code"];
	        this.category = source["category"];
	        this.message = source["message"];
	        this.retryable = source["retryable"];
	        this.userAction = source["userAction"];
	        this.cause = source["cause"];
	        this.metadata = source["metadata"];
	    }
	}
	export class TaskProgressSummary {
	    percent: number;
	    bytesLoaded?: number;
	    bytesTotal?: number;
	    currentStage?: string;
	    stageLabel?: string;
	    itemsDone?: number;
	    itemsTotal?: number;
	
	    static createFrom(source: any = {}) {
	        return new TaskProgressSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.percent = source["percent"];
	        this.bytesLoaded = source["bytesLoaded"];
	        this.bytesTotal = source["bytesTotal"];
	        this.currentStage = source["currentStage"];
	        this.stageLabel = source["stageLabel"];
	        this.itemsDone = source["itemsDone"];
	        this.itemsTotal = source["itemsTotal"];
	    }
	}

}

export namespace xiaohongshu {
	
	export class XHSImage {
	    URL: string;
	    BackupURLs: string[];
	    Width: number;
	    Height: number;
	    TraceId: string;
	    FileID: string;
	    LivePhoto: boolean;
	    LivePhotoURL: string;
	
	    static createFrom(source: any = {}) {
	        return new XHSImage(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.URL = source["URL"];
	        this.BackupURLs = source["BackupURLs"];
	        this.Width = source["Width"];
	        this.Height = source["Height"];
	        this.TraceId = source["TraceId"];
	        this.FileID = source["FileID"];
	        this.LivePhoto = source["LivePhoto"];
	        this.LivePhotoURL = source["LivePhotoURL"];
	    }
	}
	export class XHSInteractInfo {
	    LikedCount: string;
	    CollectedCount: string;
	    CommentCount: string;
	    ShareCount: string;
	
	    static createFrom(source: any = {}) {
	        return new XHSInteractInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.LikedCount = source["LikedCount"];
	        this.CollectedCount = source["CollectedCount"];
	        this.CommentCount = source["CommentCount"];
	        this.ShareCount = source["ShareCount"];
	    }
	}
	export class XHSStream {
	    QualityKey: string;
	    QualityName: string;
	    Width: number;
	    Height: number;
	    URL: string;
	    BackupURLs: string[];
	    Size: number;
	    Format: string;
	    FPS: number;
	    VideoCodec: string;
	    VideoBitrate: number;
	    AudioCodec: string;
	    AudioBitrate: number;
	    StreamDesc: string;
	    StreamType: number;
	    Weight: number;
	    Duration: number;
	    DefaultStream: number;
	    HDRType: number;
	    Rotate: number;
	
	    static createFrom(source: any = {}) {
	        return new XHSStream(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.QualityKey = source["QualityKey"];
	        this.QualityName = source["QualityName"];
	        this.Width = source["Width"];
	        this.Height = source["Height"];
	        this.URL = source["URL"];
	        this.BackupURLs = source["BackupURLs"];
	        this.Size = source["Size"];
	        this.Format = source["Format"];
	        this.FPS = source["FPS"];
	        this.VideoCodec = source["VideoCodec"];
	        this.VideoBitrate = source["VideoBitrate"];
	        this.AudioCodec = source["AudioCodec"];
	        this.AudioBitrate = source["AudioBitrate"];
	        this.StreamDesc = source["StreamDesc"];
	        this.StreamType = source["StreamType"];
	        this.Weight = source["Weight"];
	        this.Duration = source["Duration"];
	        this.DefaultStream = source["DefaultStream"];
	        this.HDRType = source["HDRType"];
	        this.Rotate = source["Rotate"];
	    }
	}
	export class XHSTag {
	    ID: string;
	    Name: string;
	    Type: string;
	
	    static createFrom(source: any = {}) {
	        return new XHSTag(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ID = source["ID"];
	        this.Name = source["Name"];
	        this.Type = source["Type"];
	    }
	}
	export class XHSItem {
	    Type: string;
	    ID: string;
	    Title: string;
	    Desc: string;
	    Cover: string;
	    Author: string;
	    AuthorID: string;
	    AuthorAvatar: string;
	    Timestamp: number;
	    IPLocation: string;
	    Tags: XHSTag[];
	    InteractInfo: XHSInteractInfo;
	    Streams: XHSStream[];
	    Images: XHSImage[];
	
	    static createFrom(source: any = {}) {
	        return new XHSItem(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.Type = source["Type"];
	        this.ID = source["ID"];
	        this.Title = source["Title"];
	        this.Desc = source["Desc"];
	        this.Cover = source["Cover"];
	        this.Author = source["Author"];
	        this.AuthorID = source["AuthorID"];
	        this.AuthorAvatar = source["AuthorAvatar"];
	        this.Timestamp = source["Timestamp"];
	        this.IPLocation = source["IPLocation"];
	        this.Tags = this.convertValues(source["Tags"], XHSTag);
	        this.InteractInfo = this.convertValues(source["InteractInfo"], XHSInteractInfo);
	        this.Streams = this.convertValues(source["Streams"], XHSStream);
	        this.Images = this.convertValues(source["Images"], XHSImage);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	

}

