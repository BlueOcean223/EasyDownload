export namespace api {
	
	export class VideoSpec {
	    fileFormat: string;
	    width: number;
	    height: number;
	    durationMs: number;
	
	    static createFrom(source: any = {}) {
	        return new VideoSpec(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.fileFormat = source["fileFormat"];
	        this.width = source["width"];
	        this.height = source["height"];
	        this.durationMs = source["durationMs"];
	    }
	}
	export class DetectedVideo {
	    id: string;
	    title: string;
	    cover: string;
	    url: string;
	    source: string;
	    quality: string;
	    duration: number;
	    author: string;
	    authorAvatar: string;
	    timestamp: number;
	    decodeKey: string;
	    fileSize: number;
	    width: number;
	    height: number;
	    isCurrentVideo: boolean;
	    fileFormats: string[];
	    specs: VideoSpec[];
	
	    static createFrom(source: any = {}) {
	        return new DetectedVideo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.title = source["title"];
	        this.cover = source["cover"];
	        this.url = source["url"];
	        this.source = source["source"];
	        this.quality = source["quality"];
	        this.duration = source["duration"];
	        this.author = source["author"];
	        this.authorAvatar = source["authorAvatar"];
	        this.timestamp = source["timestamp"];
	        this.decodeKey = source["decodeKey"];
	        this.fileSize = source["fileSize"];
	        this.width = source["width"];
	        this.height = source["height"];
	        this.isCurrentVideo = source["isCurrentVideo"];
	        this.fileFormats = source["fileFormats"];
	        this.specs = this.convertValues(source["specs"], VideoSpec);
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

export namespace bilibili {
	
	export class BilibiliLoginStatus {
	    code: number;
	    message: string;
	    sessData: string;
	
	    static createFrom(source: any = {}) {
	        return new BilibiliLoginStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.code = source["code"];
	        this.message = source["message"];
	        this.sessData = source["sessData"];
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

