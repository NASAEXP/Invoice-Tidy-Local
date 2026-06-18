export namespace main {
	
	export class CompatibilityReport {
	    cpu: string;
	    cores: number;
	    ram: string;
	    ramBytes: number;
	    gpu: string;
	    gpuBytes: number;
	    score: string;
	    recMode: string;
	
	    static createFrom(source: any = {}) {
	        return new CompatibilityReport(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.cpu = source["cpu"];
	        this.cores = source["cores"];
	        this.ram = source["ram"];
	        this.ramBytes = source["ramBytes"];
	        this.gpu = source["gpu"];
	        this.gpuBytes = source["gpuBytes"];
	        this.score = source["score"];
	        this.recMode = source["recMode"];
	    }
	}
	export class DocumentFieldValue {
	    documentId: string;
	    fieldKey: string;
	    value: string;
	    confidence: number;
	    regionJson: string;
	    rawSource: string;
	    // Go type: time
	    updatedAt: any;
	
	    static createFrom(source: any = {}) {
	        return new DocumentFieldValue(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.documentId = source["documentId"];
	        this.fieldKey = source["fieldKey"];
	        this.value = source["value"];
	        this.confidence = source["confidence"];
	        this.regionJson = source["regionJson"];
	        this.rawSource = source["rawSource"];
	        this.updatedAt = this.convertValues(source["updatedAt"], null);
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
	export class Document {
	    id: string;
	    templateId: string;
	    status: string;
	    sourceFileName: string;
	    storagePath: string;
	    fileHash: string;
	    duplicateOf?: string;
	    extractionEngine: string;
	    extractionMode: string;
	    // Go type: time
	    createdAt: any;
	    // Go type: time
	    updatedAt: any;
	    // Go type: time
	    approvedAt?: any;
	    // Go type: time
	    exportedAt?: any;
	    fields?: Record<string, string>;
	    fieldDetails?: DocumentFieldValue[];
	    tables?: Record<string, any>;
	
	    static createFrom(source: any = {}) {
	        return new Document(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.templateId = source["templateId"];
	        this.status = source["status"];
	        this.sourceFileName = source["sourceFileName"];
	        this.storagePath = source["storagePath"];
	        this.fileHash = source["fileHash"];
	        this.duplicateOf = source["duplicateOf"];
	        this.extractionEngine = source["extractionEngine"];
	        this.extractionMode = source["extractionMode"];
	        this.createdAt = this.convertValues(source["createdAt"], null);
	        this.updatedAt = this.convertValues(source["updatedAt"], null);
	        this.approvedAt = this.convertValues(source["approvedAt"], null);
	        this.exportedAt = this.convertValues(source["exportedAt"], null);
	        this.fields = source["fields"];
	        this.fieldDetails = this.convertValues(source["fieldDetails"], DocumentFieldValue);
	        this.tables = source["tables"];
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
	
	export class ExportResponse {
	    success: boolean;
	    message: string;
	    path: string;
	
	    static createFrom(source: any = {}) {
	        return new ExportResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.success = source["success"];
	        this.message = source["message"];
	        this.path = source["path"];
	    }
	}
	export class ImportResponse {
	    success: boolean;
	    message: string;
	    count: number;
	
	    static createFrom(source: any = {}) {
	        return new ImportResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.success = source["success"];
	        this.message = source["message"];
	        this.count = source["count"];
	    }
	}
	export class LocalPaths {
	    sqlitePath: string;
	    documentsDir: string;
	    daemonLogPath: string;
	
	    static createFrom(source: any = {}) {
	        return new LocalPaths(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.sqlitePath = source["sqlitePath"];
	        this.documentsDir = source["documentsDir"];
	        this.daemonLogPath = source["daemonLogPath"];
	    }
	}
	export class ModelStatus {
	    ok: boolean;
	    worker: string;
	    light: string;
	    standard: string;
	    device: string;
	    engine: string;
	    model: string;
	    mode: string;
	
	    static createFrom(source: any = {}) {
	        return new ModelStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.ok = source["ok"];
	        this.worker = source["worker"];
	        this.light = source["light"];
	        this.standard = source["standard"];
	        this.device = source["device"];
	        this.engine = source["engine"];
	        this.model = source["model"];
	        this.mode = source["mode"];
	    }
	}
	export class ProcessingStatus {
	    queued: number;
	    needsReview: number;
	    approved: number;
	    exported: number;
	    processing: number;
	
	    static createFrom(source: any = {}) {
	        return new ProcessingStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.queued = source["queued"];
	        this.needsReview = source["needsReview"];
	        this.approved = source["approved"];
	        this.exported = source["exported"];
	        this.processing = source["processing"];
	    }
	}
	export class TemplateField {
	    id: string;
	    templateId: string;
	    fieldKey: string;
	    label: string;
	    type: string;
	    required: boolean;
	    exportColumn: string;
	    hint: string;
	    visible: boolean;
	    sortOrder: number;
	    // Go type: time
	    createdAt: any;
	    // Go type: time
	    updatedAt: any;
	
	    static createFrom(source: any = {}) {
	        return new TemplateField(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.templateId = source["templateId"];
	        this.fieldKey = source["fieldKey"];
	        this.label = source["label"];
	        this.type = source["type"];
	        this.required = source["required"];
	        this.exportColumn = source["exportColumn"];
	        this.hint = source["hint"];
	        this.visible = source["visible"];
	        this.sortOrder = source["sortOrder"];
	        this.createdAt = this.convertValues(source["createdAt"], null);
	        this.updatedAt = this.convertValues(source["updatedAt"], null);
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

