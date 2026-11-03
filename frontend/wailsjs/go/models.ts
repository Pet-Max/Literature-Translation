export namespace app {
	
	export class ConnectionResult {
	    status: string;
	    message: string;
	    model_count?: number;
	
	    static createFrom(source: any = {}) {
	        return new ConnectionResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.status = source["status"];
	        this.message = source["message"];
	        this.model_count = source["model_count"];
	    }
	}
	export class LogLine {
	    time: string;
	    level: string;
	    msg: string;
	
	    static createFrom(source: any = {}) {
	        return new LogLine(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.time = source["time"];
	        this.level = source["level"];
	        this.msg = source["msg"];
	    }
	}
	export class RulesPatch {
	    active?: string;
	    presets?: config.RulePreset[];
	
	    static createFrom(source: any = {}) {
	        return new RulesPatch(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.active = source["active"];
	        this.presets = this.convertValues(source["presets"], config.RulePreset);
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
	export class SettingsPatch {
	    // Go type: struct { Token *string "json:\"token\""; ModelVersion *string "json:\"model_version\"" }
	    mineru?: any;
	    // Go type: struct { BaseURL *string "json:\"base_url\""; APIKey *string "json:\"api_key\""; Model *string "json:\"model\""; Concurrency *int "json:\"concurrency\"" }
	    llm?: any;
	    // Go type: struct { Bilingual *bool "json:\"bilingual\""; TOC *bool "json:\"toc\""; TwoColumn *bool "json:\"two_column\""; OutputDir *string "json:\"output_dir\"" }
	    export?: any;
	    // Go type: struct { SkipReferences *bool "json:\"skip_references\"" }
	    translate?: any;
	    // Go type: struct { Spelling *bool "json:\"spelling\""; Symbols *bool "json:\"symbols\"" }
	    repair?: any;
	    rules?: RulesPatch;
	    glossary_rows?: config.Term[];
	
	    static createFrom(source: any = {}) {
	        return new SettingsPatch(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mineru = this.convertValues(source["mineru"], Object);
	        this.llm = this.convertValues(source["llm"], Object);
	        this.export = this.convertValues(source["export"], Object);
	        this.translate = this.convertValues(source["translate"], Object);
	        this.repair = this.convertValues(source["repair"], Object);
	        this.rules = this.convertValues(source["rules"], RulesPatch);
	        this.glossary_rows = this.convertValues(source["glossary_rows"], config.Term);
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
	export class Task {
	    id: string;
	    code: string;
	    kind: string;
	    mode: string;
	    source_path: string;
	    status: string;
	    stage: string;
	    done: number;
	    total: number;
	    failed: number;
	    done_idx: number[];
	    failed_idx: number[];
	    sweeping: boolean;
	    refs_kept: number;
	    out_pdf: string;
	    zh_md: string;
	    work_dir: string;
	    err: string;
	    // Go type: time
	    added_at: any;
	
	    static createFrom(source: any = {}) {
	        return new Task(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.code = source["code"];
	        this.kind = source["kind"];
	        this.mode = source["mode"];
	        this.source_path = source["source_path"];
	        this.status = source["status"];
	        this.stage = source["stage"];
	        this.done = source["done"];
	        this.total = source["total"];
	        this.failed = source["failed"];
	        this.done_idx = source["done_idx"];
	        this.failed_idx = source["failed_idx"];
	        this.sweeping = source["sweeping"];
	        this.refs_kept = source["refs_kept"];
	        this.out_pdf = source["out_pdf"];
	        this.zh_md = source["zh_md"];
	        this.work_dir = source["work_dir"];
	        this.err = source["err"];
	        this.added_at = this.convertValues(source["added_at"], null);
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
	export class UpdateInfo {
	    has_update: boolean;
	    current: string;
	    latest: string;
	    url: string;
	
	    static createFrom(source: any = {}) {
	        return new UpdateInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.has_update = source["has_update"];
	        this.current = source["current"];
	        this.latest = source["latest"];
	        this.url = source["url"];
	    }
	}

}

export namespace config {
	
	export class UserFont {
	    internal_name: string;
	    display_name: string;
	    file_name: string;
	
	    static createFrom(source: any = {}) {
	        return new UserFont(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.internal_name = source["internal_name"];
	        this.display_name = source["display_name"];
	        this.file_name = source["file_name"];
	    }
	}
	export class Translate {
	    skip_references: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Translate(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.skip_references = source["skip_references"];
	    }
	}
	export class Repair {
	    spelling: boolean;
	    symbols: boolean;
	
	    static createFrom(source: any = {}) {
	        return new Repair(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.spelling = source["spelling"];
	        this.symbols = source["symbols"];
	    }
	}
	export class Export {
	    bilingual: boolean;
	    toc: boolean;
	    two_column: boolean;
	    font_preset: string;
	    font: string;
	    user_font: string;
	    output_dir: string;
	
	    static createFrom(source: any = {}) {
	        return new Export(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.bilingual = source["bilingual"];
	        this.toc = source["toc"];
	        this.two_column = source["two_column"];
	        this.font_preset = source["font_preset"];
	        this.font = source["font"];
	        this.user_font = source["user_font"];
	        this.output_dir = source["output_dir"];
	    }
	}
	export class RulePreset {
	    name: string;
	    text: string;
	    draft_text?: string;
	
	    static createFrom(source: any = {}) {
	        return new RulePreset(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.text = source["text"];
	        this.draft_text = source["draft_text"];
	    }
	}
	export class Rules {
	    active: string;
	    presets: RulePreset[];
	    user_global: string;
	
	    static createFrom(source: any = {}) {
	        return new Rules(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.active = source["active"];
	        this.presets = this.convertValues(source["presets"], RulePreset);
	        this.user_global = source["user_global"];
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
	export class Term {
	    en: string;
	    zh: string;
	
	    static createFrom(source: any = {}) {
	        return new Term(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.en = source["en"];
	        this.zh = source["zh"];
	    }
	}
	export class LLM {
	    base_url: string;
	    api_key: string;
	    model: string;
	    concurrency: number;
	
	    static createFrom(source: any = {}) {
	        return new LLM(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.base_url = source["base_url"];
	        this.api_key = source["api_key"];
	        this.model = source["model"];
	        this.concurrency = source["concurrency"];
	    }
	}
	export class MinerU {
	    token: string;
	    model_version: string;
	
	    static createFrom(source: any = {}) {
	        return new MinerU(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.token = source["token"];
	        this.model_version = source["model_version"];
	    }
	}
	export class Config {
	    mineru: MinerU;
	    llm: LLM;
	    glossary: Term[];
	    glossary_rows?: Term[];
	    rules: Rules;
	    export: Export;
	    repair: Repair;
	    translate: Translate;
	    user_fonts: UserFont[];
	
	    static createFrom(source: any = {}) {
	        return new Config(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.mineru = this.convertValues(source["mineru"], MinerU);
	        this.llm = this.convertValues(source["llm"], LLM);
	        this.glossary = this.convertValues(source["glossary"], Term);
	        this.glossary_rows = this.convertValues(source["glossary_rows"], Term);
	        this.rules = this.convertValues(source["rules"], Rules);
	        this.export = this.convertValues(source["export"], Export);
	        this.repair = this.convertValues(source["repair"], Repair);
	        this.translate = this.convertValues(source["translate"], Translate);
	        this.user_fonts = this.convertValues(source["user_fonts"], UserFont);
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
	
	
	export class LoadState {
	    ready: boolean;
	    message: string;
	    has_backup: boolean;
	
	    static createFrom(source: any = {}) {
	        return new LoadState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ready = source["ready"];
	        this.message = source["message"];
	        this.has_backup = source["has_backup"];
	    }
	}
	
	
	
	
	
	

}

