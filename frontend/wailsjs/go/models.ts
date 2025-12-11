export namespace api {
	
	export class DetectedVideo {
	    id: string;
	    title: string;
	    cover: string;
	    url: string;
	    source: string;
	    quality: string;
	    duration: number;
	    author: string;
	    timestamp: number;
	
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
	        this.timestamp = source["timestamp"];
	    }
	}

}

export namespace downloader {
	
	export class BilibiliPart {
	    cid: number;
	    page: number;
	    partName: string;
	    duration: number;
	
	    static createFrom(source: any = {}) {
	        return new BilibiliPart(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.cid = source["cid"];
	        this.page = source["page"];
	        this.partName = source["partName"];
	        this.duration = source["duration"];
	    }
	}
	export class BilibiliStream {
	    quality: number;
	    qualityName: string;
	    format: string;
	    size: number;
	    videoUrl: string;
	    audioUrl: string;
	
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

