// app.js - Alpine.js Core Controller for Invoice Tidy Local

function appController() {
  return {
    currentView: 'workspace', // 'workspace', 'review', 'compatibility', 'settings'
    sidebarCollapsed: false,
    documents: [],
    fields: [],
    currentDoc: null,
    currentDocBase64: '',
    queueStatus: { queued: 0, needsReview: 0, approved: 0, exported: 0, processing: 0 },
    diagnostics: {
      ramBytes: 0,
      ramFormatted: 'Loading...',
      cpuCores: 0,
      cpuModel: 'Loading...',
      gpuController: 'Loading...',
      gpuVramBytes: 0,
      gpuVramFormatted: 'None',
      grade: 'light_mode',
      reason: 'Assessing system diagnostics...'
    },
    modelStatus: { ok: false, worker: 'starting', light: 'not_loaded', standard: 'not_loaded', device: 'cpu', engine: 'docling-layout-rapidocr', model: 'docling-local-layout' },
    activeHighlight: { top: '0%', left: '0%', width: '0%', height: '0%', opacity: 0 },
    pinnedHighlightField: '',
    hoverHighlightField: '',
    previewNaturalSize: { width: 0, height: 0 },
    previewPage: 1,
    coordinateMap: {
      vendor_name: { top: '7.5%', left: '3.5%', width: '56%', height: '14.5%' },
      invoice_number: { top: '7.5%', left: '69%', width: '27%', height: '8.5%' },
      invoice_date: { top: '16.5%', left: '67.5%', width: '29%', height: '4.5%' },
      due_date: { top: '21%', left: '67.5%', width: '29%', height: '4.5%' },
      currency: { top: '39.5%', left: '3.5%', width: '92.5%', height: '10%' },
      subtotal: { top: '74.5%', left: '54.5%', width: '42.5%', height: '3.8%' },
      tax: { top: '78.5%', left: '54.5%', width: '42.5%', height: '3.8%' },
      total: { top: '82.5%', left: '54.5%', width: '42.5%', height: '6.5%' }
    },
    activeTab: 'tab-general',
    mode: 'light',
    
    // Setup & Daemon Log states
    setupInProgress: false,
    setupLogs: [],
    daemonLogs: 'Loading daemon logs...',
    localPaths: { sqlitePath: '', documentsDir: '', daemonLogPath: '' },
    
    // In-row processing updates
    processingProgress: {}, // map of docID -> { progress, message }
    selectedDocumentIds: {},
    
    // Field editor modal / page states
    newField: { fieldKey: '', label: '', type: 'text', exportColumn: '', hint: '', required: false },

    async init() {
      await Promise.all([
        this.loadDiagnostics(),
        this.loadModelStatus(),
        this.loadFields(),
        this.loadDocuments(),
        this.loadQueueStatus(),
        this.loadLocalPaths(),
      ]);

      // 2. Wire up Wails setup progress logs
      window.runtime.EventsOn("local:setupProgress", (text) => {
        console.log("Setup Progress log:", text);
        this.setupInProgress = true;
        this.setupLogs.push(text);
        
        // Auto scroll setup log console
        this.$nextTick(() => {
          const consoleElem = document.getElementById("setup-console");
          if (consoleElem) {
            consoleElem.scrollTop = consoleElem.scrollHeight;
          }
        });

        // Setup complete triggers
        if (text.includes("Setup completed successfully") || text.includes("completed successfully")) {
          setTimeout(() => {
            this.setupInProgress = false;
            this.triggerToast("Local parser setup completed!");
            this.loadModelStatus();
          }, 2000);
        }
      });

      // 3. Wire up real-time document parsing progress
      window.runtime.EventsOn("local:processingProgress", (event) => {
        console.log("Processing progress event:", event);
        if (event && event.documentId) {
          const docId = event.documentId;
          this.processingProgress[docId] = {
            status: event.status,
            progress: event.progress * 100,
            message: event.message
          };
          
          if (event.status === 'needs_review' || event.status === 'failed') {
            // Delete key to clear row animation once done
            setTimeout(() => {
              delete this.processingProgress[docId];
              this.loadDocuments();
              this.loadQueueStatus();
            }, 1500);
          }
        }
        // General fallback refresh
        this.loadDocuments();
        this.loadQueueStatus();
      });

      this.registerFileDrop();
      
      // Auto-poll model status every 10 seconds to keep track of engine warmup/readiness
      setInterval(async () => {
        await this.loadModelStatus();
      }, 10000);
    },

    async loadDiagnostics() {
      try {
        this.diagnostics = await window.go.main.App.GetCompatibility();
      } catch (err) {
        console.error("Failed to load hardware diagnostics:", err);
      }
    },

    async loadModelStatus() {
      try {
        this.modelStatus = await window.go.main.App.GetModelStatus();
        if (this.modelStatus.mode && this.modelStatus.mode !== 'auto') {
          this.mode = this.modelStatus.mode;
        } else if (!this.mode) {
          this.mode = this.modelStatus.worker === 'ready' && this.modelStatus.standard === 'loaded' ? 'standard' : 'light';
        }
        if (this.modelStatus.worker === 'starting' || this.modelStatus.worker === 'warming') {
          setTimeout(async () => {
            await this.loadModelStatus();
          }, 1500);
        }
      } catch (err) {
        console.error("Failed to load model status:", err);
      }
    },

    async loadFields() {
      try {
        this.fields = await window.go.main.App.ListFields();
      } catch (err) {
        console.error("Failed to load template fields:", err);
      }
    },

    async loadDocuments() {
      try {
        this.documents = await window.go.main.App.ListDocuments();
        this.pruneSelectedDocuments();
      } catch (err) {
        console.error("Failed to load documents list:", err);
      }
    },

    async loadQueueStatus() {
      try {
        this.queueStatus = await window.go.main.App.GetProcessingStatus();
      } catch (err) {
        console.error("Failed to load processing stats:", err);
      }
    },

    async loadLocalPaths() {
      try {
        this.localPaths = await window.go.main.App.GetLocalPaths();
      } catch (err) {
        console.error("Failed to load local paths:", err);
      }
    },

    isDocumentSelected(documentID) {
      return !!this.selectedDocumentIds[documentID];
    },

    selectedDocumentCount() {
      return Object.values(this.selectedDocumentIds).filter(Boolean).length;
    },

    selectedDocumentIDList() {
      return Object.keys(this.selectedDocumentIds).filter(id => this.selectedDocumentIds[id]);
    },

    allDocumentsSelected() {
      return this.documents.length > 0 && this.documents.every(doc => this.isDocumentSelected(doc.id));
    },

    toggleDocumentSelection(documentID, selected) {
      const next = { ...this.selectedDocumentIds };
      if (selected) {
        next[documentID] = true;
      } else {
        delete next[documentID];
      }
      this.selectedDocumentIds = next;
    },

    toggleAllDocuments(selected) {
      const next = { ...this.selectedDocumentIds };
      this.documents.forEach(doc => {
        if (selected) {
          next[doc.id] = true;
        } else {
          delete next[doc.id];
        }
      });
      this.selectedDocumentIds = next;
    },

    pruneSelectedDocuments() {
      const validIDs = new Set(this.documents.map(doc => doc.id));
      const next = {};
      Object.keys(this.selectedDocumentIds).forEach(id => {
        if (validIDs.has(id)) next[id] = true;
      });
      this.selectedDocumentIds = next;
    },

    async deleteSelectedDocuments() {
      const ids = this.selectedDocumentIDList();
      if (ids.length === 0) return;

      const noun = ids.length === 1 ? "invoice" : "invoices";
      if (!window.confirm(`Delete ${ids.length} selected ${noun}? This removes the local stored file(s) too.`)) {
        return;
      }

      try {
        if (window.go.main.App.DeleteDocuments) {
          this.documents = await window.go.main.App.DeleteDocuments(ids);
        } else {
          for (const id of ids) {
            this.documents = await window.go.main.App.DeleteDocument(id);
          }
        }
        ids.forEach(id => {
          delete this.processingProgress[id];
        });
        this.selectedDocumentIds = {};
        await this.loadQueueStatus();
        this.triggerToast(`Deleted ${ids.length} ${noun}.`);
      } catch (err) {
        this.triggerToast(`Delete failed: ${err}`);
      }
    },

    async chooseAndImport() {
      try {
        this.triggerToast(`Opening native file chooser...`);
        const resp = await window.go.main.App.ChooseAndImport(this.mode);
        this.showImportResponse(resp, "No files selected.");
        await this.loadDocuments();
        await this.loadQueueStatus();
      } catch (err) {
        this.triggerToast(`Failed to trigger import: ${err}`);
      }
    },

    registerFileDrop() {
      if (!window.runtime || typeof window.runtime.OnFileDrop !== 'function') {
        return;
      }

      window.runtime.OnFileDrop(async (_x, _y, paths) => {
        await this.importDroppedPaths(paths || []);
      }, true);
    },

    async importDroppedPaths(paths) {
      if (!paths || paths.length === 0) {
        this.triggerToast("No files were dropped.");
        return;
      }

      if (!window.go || !window.go.main || !window.go.main.App || !window.go.main.App.ImportDroppedFiles) {
        this.triggerToast("Drop import is not available yet. Restart the app after the update.");
        return;
      }

      try {
        this.triggerToast(`Importing ${paths.length} dropped file(s)...`);
        const resp = await window.go.main.App.ImportDroppedFiles(paths, this.mode);
        this.showImportResponse(resp, "No supported files were dropped.");
        await this.loadDocuments();
        await this.loadQueueStatus();
      } catch (err) {
        this.triggerToast(`Drop import failed: ${err}`);
      }
    },

    showImportResponse(resp, emptyMessage) {
      if (resp && resp.success) {
        if (resp.count > 0) {
          this.triggerToast(resp.message || `Successfully imported ${resp.count} file(s) for local extraction.`);
        } else {
          this.triggerToast(emptyMessage);
        }
      } else {
        this.triggerToast(`Import error: ${(resp && resp.message) || "Unable to import files."}`);
      }
    },

    async exportCSV() {
      try {
        this.triggerToast("Exporting approved invoices to Excel-friendly CSV...");
        const resp = await window.go.main.App.ExportCSV();
        if (resp.success) {
          if (resp.path) {
            this.triggerToast(`Successfully exported approved rows to: ${resp.path}`);
          } else {
            this.triggerToast("Export canceled.");
          }
        } else {
          this.triggerToast(`Export failed: ${resp.message}`);
        }
        await this.loadDocuments();
        await this.loadQueueStatus();
      } catch (err) {
        this.triggerToast(`Export error: ${err}`);
      }
    },

    async toggleFieldVisibility(field) {
      try {
        this.fields = await window.go.main.App.SetFieldVisibility(field.fieldKey, !field.visible);
        this.triggerToast(`Updated visibility for field ${field.label}`);
      } catch (err) {
        this.triggerToast(`Failed to toggle visibility: ${err}`);
      }
    },

    async saveCustomField() {
      if (!this.newField.fieldKey || !this.newField.label) {
        this.triggerToast("Please fill out Key and Label fields.");
        return;
      }
      // Sanitize key
      this.newField.fieldKey = this.newField.fieldKey.toLowerCase().replace(/[^a-z0-9_]/g, '');
      if (!this.newField.exportColumn) {
        this.newField.exportColumn = this.newField.label;
      }
      try {
        this.fields = await window.go.main.App.SaveField(this.newField);
        this.triggerToast(`Custom field '${this.newField.label}' added successfully.`);
        // Reset
        this.newField = { fieldKey: '', label: '', type: 'text', exportColumn: '', hint: '', required: false };
      } catch (err) {
        this.triggerToast(`Failed to save field: ${err}`);
      }
    },

    async setModelMode(targetMode) {
      this.mode = targetMode;
      try {
        this.modelStatus = await window.go.main.App.SetModelMode(targetMode);
        this.triggerToast(`Extraction mode set to: ${targetMode}`);
      } catch (err) {
        console.error("Failed to set model mode:", err);
      }
    },

    // Sidebar navigation controller
    switchView(viewName) {
      this.currentView = viewName;
      if (viewName === 'settings') {
        this.loadDaemonLogs();
      }
    },

    toggleSidebar() {
      this.sidebarCollapsed = !this.sidebarCollapsed;
    },

    // Review Screen Operations
    async startReview(doc) {
      this.currentDoc = doc;
      this.currentView = 'review';
      this.activeTab = 'tab-general';
      this.currentDocBase64 = 'loading';
      this.pinnedHighlightField = '';
      this.hoverHighlightField = '';
      this.previewNaturalSize = { width: 0, height: 0 };
      this.previewPage = 1;
      this.clearFieldHighlight();
      
      // Load file binary in base64
      try {
        this.currentDocBase64 = await window.go.main.App.OpenDocumentFile(doc.storagePath);
      } catch (err) {
        console.error("Failed to load invoice binary:", err);
        this.currentDocBase64 = 'error';
      }

      this.$nextTick(() => this.refreshActiveHighlight());
    },

    focusField(fieldName) {
      this.previewFieldHighlight(fieldName);
    },

    previewFieldHighlight(fieldName) {
      this.hoverHighlightField = fieldName;
      this.refreshActiveHighlight();
    },

    clearPreviewFieldHighlight(fieldName) {
      if (this.hoverHighlightField === fieldName) {
        this.hoverHighlightField = '';
      }
      this.refreshActiveHighlight();
    },

    pinFieldHighlight(fieldName) {
      this.pinnedHighlightField = fieldName;
      this.hoverHighlightField = '';
      this.refreshActiveHighlight();
    },

    clearFieldHighlight() {
      this.activeHighlight = { top: '0%', left: '0%', width: '0%', height: '0%', opacity: 0 };
    },

    refreshActiveHighlight() {
      const fieldName = this.hoverHighlightField || this.pinnedHighlightField;
      if (!fieldName) {
        this.clearFieldHighlight();
        return;
      }

      const highlight = this.normalizedHighlightForField(fieldName);
      if (highlight) {
        this.activeHighlight = { ...highlight, opacity: 1 };
      } else {
        this.clearFieldHighlight();
      }
    },

    capturePreviewImageSize(event) {
      const image = event?.target;
      this.previewNaturalSize = {
        width: image?.naturalWidth || 0,
        height: image?.naturalHeight || 0,
      };
      this.refreshActiveHighlight();
    },

    fieldDetail(fieldName) {
      return (this.currentDoc?.fieldDetails || []).find(detail => detail.fieldKey === fieldName) || null;
    },

    fieldRegion(fieldName) {
      const raw = this.fieldDetail(fieldName)?.regionJson;
      if (!raw) return null;
      if (typeof raw === 'object') return raw;
      try {
        return JSON.parse(raw);
      } catch (_err) {
        return null;
      }
    },

    normalizedHighlightForField(fieldName) {
      const region = this.fieldRegion(fieldName);
      const box = region?.box;
      if (box) {
        const normalized = this.normalizeHighlightBox(box, region);
        if (normalized) return normalized;
      }
      return this.coordinateMap[fieldName] || null;
    },

    normalizeHighlightBox(box, region) {
      const x = Number(box.x ?? box.left ?? box.l ?? 0);
      const y = Number(box.y ?? box.top ?? box.t ?? 0);
      const width = Number(box.width ?? ((box.r ?? box.right ?? 0) - (box.l ?? box.left ?? x)));
      const height = Number(box.height ?? ((box.b ?? box.bottom ?? 0) - (box.t ?? box.top ?? y)));
      if (![x, y, width, height].every(Number.isFinite) || width <= 0 || height <= 0) return null;

      const page = Number(region?.page || box.page || 1);
      if (Number.isFinite(page) && page > 0 && page !== this.previewPage) return null;

      if (Math.max(x, y, width, height) <= 1) {
        return this.highlightPercentBox(x * 100, y * 100, width * 100, height * 100);
      }
      if (Math.max(x, y, width, height) <= 100) {
        return this.highlightPercentBox(x, y, width, height);
      }

      const pageWidth = Number(region?.pageWidth || region?.page_width || box.pageWidth || box.page_width || this.previewNaturalSize.width || 0);
      const pageHeight = Number(region?.pageHeight || region?.page_height || box.pageHeight || box.page_height || this.previewNaturalSize.height || 0);
      if (!pageWidth || !pageHeight) return null;

      const origin = String(box.coordOrigin || box.coord_origin || region?.coordOrigin || region?.coord_origin || '').toUpperCase();
      const top = origin.includes('TOPLEFT') ? y : pageHeight - y - height;
      return this.highlightPercentBox(
        (x / pageWidth) * 100,
        (top / pageHeight) * 100,
        (width / pageWidth) * 100,
        (height / pageHeight) * 100,
      );
    },

    highlightPercentBox(left, top, width, height) {
      const clamp = (value, min, max) => Math.max(min, Math.min(max, value));
      const safeLeft = clamp(left, 0, 99);
      const safeTop = clamp(top, 0, 99);
      return {
        left: `${safeLeft}%`,
        top: `${safeTop}%`,
        width: `${clamp(width, 0.5, 100 - safeLeft)}%`,
        height: `${clamp(height, 0.5, 100 - safeTop)}%`,
      };
    },

    highlightStyle() {
      return `left: ${this.activeHighlight.left}; top: ${this.activeHighlight.top}; width: ${this.activeHighlight.width}; height: ${this.activeHighlight.height}; opacity: ${this.activeHighlight.opacity};`;
    },

    isPreviewImage() {
      return typeof this.currentDocBase64 === 'string' && this.currentDocBase64.startsWith('data:image/');
    },

    isPreviewPdf() {
      return typeof this.currentDocBase64 === 'string' && this.currentDocBase64.startsWith('data:application/pdf');
    },

    async updateFieldValue(fieldKey, value) {
      if (this.currentDoc) {
        try {
          this.currentDoc.fields = this.currentDoc.fields || {};
          this.currentDoc.fields[fieldKey] = value;
          
          // Call Go IPC to update
          await window.go.main.App.UpdateFieldValue(this.currentDoc.id, fieldKey, value);
        } catch (err) {
          console.error("Failed to save field correction:", err);
        }
      }
    },

    async approveAndNext() {
      if (this.currentDoc) {
        try {
          await window.go.main.App.SetStatus(this.currentDoc.id, 'approved');
          this.triggerToast(`Approved: ${this.currentDoc.sourceFileName}`);
          
          await this.loadDocuments();
          await this.loadQueueStatus();

          // Find next document in queue
          const nextDoc = this.documents.find(d => d.status === 'needs_review' && d.id !== this.currentDoc.id);
          if (nextDoc) {
            this.startReview(nextDoc);
          } else {
            this.currentView = 'workspace';
            this.currentDoc = null;
          }
        } catch (err) {
          this.triggerToast(`Failed to approve document: ${err}`);
        }
      }
    },

    async skipDocument() {
      if (this.currentDoc) {
        const nextDoc = this.documents.find(d => d.status === 'needs_review' && d.id !== this.currentDoc.id);
        if (nextDoc) {
          this.startReview(nextDoc);
        } else {
          this.currentView = 'workspace';
          this.currentDoc = null;
        }
        this.triggerToast("Skipped transaction.");
      }
    },

    async reprocessCurrentDoc() {
      if (this.currentDoc) {
        try {
          this.triggerToast(`Reprocessing document with ${this.mode} mode...`);
          await window.go.main.App.ReprocessDocuments([this.currentDoc.id], this.mode);
          this.currentView = 'workspace';
          this.currentDoc = null;
        } catch (err) {
          this.triggerToast(`Reprocess failed: ${err}`);
        }
      }
    },

    async deleteCurrentDoc() {
      if (this.currentDoc) {
        try {
          const deletedId = this.currentDoc.id;
          await window.go.main.App.DeleteDocument(this.currentDoc.id);
          delete this.processingProgress[deletedId];
          this.triggerToast("Invoice record deleted.");
          await this.loadDocuments();
          await this.loadQueueStatus();
          
          const nextDoc = this.documents.find(d => d.status === 'needs_review');
          if (nextDoc) {
            this.startReview(nextDoc);
          } else {
            this.currentView = 'workspace';
            this.currentDoc = null;
          }
        } catch (err) {
          this.triggerToast(`Delete failed: ${err}`);
        }
      }
    },

    // Read-only line item display from Docling table extraction.
    rawExtractedTables() {
      const tables = this.currentDoc?.tables || {};
      return Object.values(tables).filter(Boolean);
    },

    tableGrid(table) {
      const rawGrid = table?.data?.data?.grid || table?.data?.grid || table?.grid || table?.rows || [];
      if (!Array.isArray(rawGrid)) return [];
      return rawGrid
        .filter(row => Array.isArray(row))
        .map(row => row.map(cell => {
          if (cell && typeof cell === 'object') {
            return String(cell.text || cell.value || '').trim();
          }
          return String(cell || '').trim();
        }));
    },

    bestLineItemGrid() {
      const grids = this.rawExtractedTables().map(table => this.tableGrid(table)).filter(grid => grid.length >= 2);
      if (grids.length === 0) return [];
      return grids.sort((a, b) => b.length - a.length)[0];
    },

    lineItemHeaderIndex(grid) {
      let best = { index: 0, score: 0 };
      grid.forEach((row, rowIndex) => {
        const roles = row.map((header, index) => this.rawLineItemColumnRole(header, index, grid, rowIndex)).filter(Boolean);
        const uniqueRoles = new Set(roles);
        let score = uniqueRoles.size;
        if (uniqueRoles.has('description')) score += 3;
        if (uniqueRoles.has('total')) score += 2;
        if (uniqueRoles.has('rate')) score += 1;
        if (uniqueRoles.has('quantity')) score += 1;
        if (this.isSummaryLineItemRow(row)) score -= 6;
        if (score > best.score) best = { index: rowIndex, score };
      });
      return best.score >= 3 ? best.index : 0;
    },

    rawLineItemColumnRole(header, index, grid, headerIndex) {
      const key = this.normalizedLineItemHeader(header);
      const headers = grid[headerIndex] || [];
      const allHeaders = headers.map(h => this.normalizedLineItemHeader(h));
      const values = grid.slice(headerIndex + 1).map(row => String(row[index] || ''));
      const hasTotalColumn = allHeaders.some(h => h.includes('total') || h.includes('extension'));
      const valuesLookLikeHours = values.some(value => /\d\s*(h|hr|hrs|hours)\b/i.test(value));

      if (key === 'date' || key.endsWith('date')) return 'date';
      if (key.includes('description') || key.includes('service') || key.includes('work') || key.includes('activity')) return 'description';
      if (key.includes('item') || key.includes('line')) return 'item';
      if (key === 'qty' || key.includes('quantity') || key.includes('hours') || key === 'hrs') return 'quantity';
      if (key.includes('amount')) return (valuesLookLikeHours || hasTotalColumn) ? 'quantity' : 'total';
      if (key.includes('unitprice') || key.includes('priceperunit') || key.includes('rate')) return 'rate';
      if (key.includes('total') || key.includes('extension')) return 'total';
      if (index === 0 && !key.includes('invoice')) return 'item';
      return '';
    },

    lineItemColumnLabel(role, rawHeader, values) {
      const key = this.normalizedLineItemHeader(rawHeader);
      if (role === 'date') return 'Date';
      if (role === 'item') return 'Item';
      if (role === 'description') return 'Description';
      if (role === 'quantity') return 'Qty';
      if (role === 'rate') return 'Rate';
      if (role === 'total') return 'Total';
      return rawHeader || 'Detail';
    },

    lineItemDisplayRoles() {
      return ['description', 'quantity', 'rate', 'total'];
    },

    lineItemDisplayLabels() {
      return {
        description: 'Description',
        quantity: 'Qty',
        rate: 'Rate',
        total: 'Total',
      };
    },

    lineItemData() {
      const grid = this.bestLineItemGrid();
      if (grid.length < 2) return { headers: [], rows: [] };
      const sectionData = this.sectionedLineItemData(grid);
      if (sectionData.rows.length > 0) return sectionData;

      const headerIndex = this.lineItemHeaderIndex(grid);
      const columnsByRole = {};

      this.lineItemColumnsFromHeader(grid[headerIndex] || [], grid, headerIndex).forEach(column => {
        if (!columnsByRole[column.role]) columnsByRole[column.role] = column;
      });

      const columns = ['date', 'item', 'description', 'quantity', 'rate', 'total'].map(role => columnsByRole[role]).filter(Boolean);
      if (!columns.some(column => column.role === 'description')) return { headers: [], rows: [] };

      const rowObjects = grid.slice(headerIndex + 1)
        .filter(row => !this.isSummaryLineItemRow(row))
        .map(row => this.extractLineItemObject(row, columns))
        .filter(item => item && item.description && (item.total || item.quantity || item.rate));

      return this.normalizedLineItemData(rowObjects);
    },

    sectionedLineItemData(grid) {
      const rowObjects = [];
      let activeColumns = null;

      grid.forEach((row, rowIndex) => {
        const columns = this.lineItemColumnsFromHeader(row, grid, rowIndex);
        if (this.isLikelyLineItemHeaderRow(row, columns)) {
          activeColumns = columns;
          return;
        }
        if (!activeColumns || this.isSummaryLineItemRow(row) || this.isRepeatedSectionLabelRow(row)) return;

        const item = this.extractLineItemObject(row, activeColumns);
        if (item && item.description && (item.total || item.quantity || item.date)) {
          rowObjects.push(item);
        }
      });

      if (rowObjects.length === 0) return { headers: [], rows: [] };

      return this.normalizedLineItemData(rowObjects);
    },

    normalizedLineItemData(rowObjects) {
      const roles = this.lineItemDisplayRoles();
      const labels = this.lineItemDisplayLabels();
      return {
        headers: roles.map(role => labels[role]),
        rows: rowObjects
          .map(item => roles.map(role => this.displayLineItemRoleValue(role, item[role])))
          .filter(row => String(row[0] || '').trim()),
      };
    },

    lineItemColumnsFromHeader(row, grid, rowIndex) {
      const seenRoles = {};
      const columns = row.map((header, index) => {
        const role = this.rawLineItemColumnRole(header, index, grid, rowIndex);
        if (!role || seenRoles[role]) return null;
        seenRoles[role] = true;
        const values = grid.slice(rowIndex + 1).map(nextRow => nextRow[index] || '');
        return {
          index,
          role,
          label: this.lineItemColumnLabel(role, header, values),
        };
      }).filter(Boolean);
      return this.repairShiftedLineItemColumns(columns, grid, rowIndex);
    },

    repairShiftedLineItemColumns(columns, grid, rowIndex) {
      if (columns.some(column => column.role === 'date')) return columns;

      const dataRows = grid
        .slice(rowIndex + 1)
        .filter(row => Array.isArray(row) && !this.isSummaryLineItemRow(row) && !this.isRepeatedSectionLabelRow(row));
      if (dataRows.length === 0) return columns;

      const firstColumnHasDates = dataRows.some(row => this.dateFromLineItemCell(row[0]));
      const secondColumnHasDescription = dataRows.some(row => {
        const value = String(row[1] || '').trim();
        return value && !this.isNonDescriptionLineItemCell(value);
      });
      if (!firstColumnHasDates || !secondColumnHasDescription) return columns;

      const maxColumns = Math.max(...dataRows.map(row => row.length), 0);
      const originalRoles = new Set(columns.map(column => column.role));
      const headerSuggestsAmount = originalRoles.has('rate') || originalRoles.has('total');
      const totalIndex = headerSuggestsAmount
        ? this.findLineItemDataColumn(dataRows, (value) => this.isLineItemAmountCell(value), [0, 1], true)
        : -1;
      const quantityIndex = this.findLineItemDataColumn(
        dataRows,
        (value) => this.isNumericCell(value) || /\d\s*(h|hr|hrs|hours)\b/i.test(String(value || '')),
        [0, 1, totalIndex],
        false,
      );
      const rateIndex = this.findLineItemDataColumn(
        dataRows,
        (value) => this.isLineItemAmountCell(value),
        [0, 1, quantityIndex, totalIndex],
        false,
      );

      const repaired = [
        { index: 0, role: 'date', label: 'Date' },
        { index: 1, role: 'description', label: 'Description' },
      ];
      if (quantityIndex > 1 && quantityIndex < maxColumns) {
        repaired.push({ index: quantityIndex, role: 'quantity', label: 'Qty' });
      }
      if (rateIndex > 1 && rateIndex < maxColumns) {
        repaired.push({ index: rateIndex, role: 'rate', label: 'Rate' });
      }
      if (totalIndex > 1 && totalIndex < maxColumns) {
        repaired.push({ index: totalIndex, role: 'total', label: 'Total' });
      }

      const seenRoles = new Set();
      return repaired.filter(column => {
        if (seenRoles.has(column.role)) return false;
        seenRoles.add(column.role);
        return true;
      });
    },

    findLineItemDataColumn(rows, predicate, excludedIndexes, preferRight) {
      const excluded = new Set(excludedIndexes.filter(index => Number.isInteger(index) && index >= 0));
      const maxColumns = Math.max(...rows.map(row => row.length), 0);
      const indexes = Array.from({ length: maxColumns }, (_, index) => index).filter(index => !excluded.has(index));
      if (preferRight) indexes.reverse();
      return indexes.find(index => rows.some(row => predicate(String(row[index] || '').trim()))) ?? -1;
    },

    isLikelyLineItemHeaderRow(row, columns) {
      if (columns.length < 3 || !columns.some(column => column.role === 'description')) return false;
      if (row.some(cell => this.dateFromLineItemCell(cell) || this.isMoneyCell(cell))) return false;
      const joined = row.map(cell => this.normalizedLineItemHeader(cell)).join('');
      return /date|description|service|work|item|qty|quantity|hours|hrs|rate|extension|total|amount|price/.test(joined);
    },

    extractLineItemObject(row, columns) {
      const byRole = Object.fromEntries(columns.map(column => [column.role, column]));
      const rawDescription = this.extractLineItemDescription(row, byRole);
      const description = this.cleanLineItemDescription(rawDescription);
      if (!description) return null;

      return {
        date: this.extractLineItemDate(row, byRole),
        item: this.extractLineItemDirect(row, byRole.item),
        description,
        quantity: this.extractLineItemQuantity(row, byRole.quantity, rawDescription),
        rate: this.extractLineItemDirect(row, byRole.rate),
        total: this.extractLineItemTotal(row, byRole.total),
      };
    },

    extractLineItemDirect(row, column) {
      if (!column) return '';
      return String(row[column.index] || '').trim();
    },

    extractLineItemDate(row, byRole) {
      const raw = this.extractLineItemDirect(row, byRole.date) || row.find(cell => this.dateFromLineItemCell(cell)) || '';
      return this.dateFromLineItemCell(raw);
    },

    dateFromLineItemCell(value) {
      const match = String(value || '').match(/\b\d{1,2}\/\d{1,2}\/\d{2,4}\b|\b[A-Z][a-z]{2,9}\s+\d{1,2},?\s+\d{4}\b/);
      return match ? match[0] : '';
    },

    extractLineItemDescription(row, byRole) {
      const mapped = this.extractLineItemDirect(row, byRole.description);
      if (mapped && !this.isNonDescriptionLineItemCell(mapped)) return mapped;

      const used = new Set(Object.values(byRole).filter(Boolean).map(column => column.index));
      const candidates = row
        .map((cell, index) => ({ index, value: String(cell || '').trim() }))
        .filter(candidate => candidate.value && !used.has(candidate.index))
        .filter(candidate => !this.isNonDescriptionLineItemCell(candidate.value))
        .sort((a, b) => b.value.length - a.value.length);
      return candidates[0]?.value || mapped || '';
    },

    isNonDescriptionLineItemCell(value) {
      const text = String(value || '').trim();
      const key = this.normalizedLineItemHeader(text);
      if (!text) return true;
      if (this.dateFromLineItemCell(text) === text) return true;
      if (this.isMoneyCell(text) || this.isNumericCell(text) || /%$/.test(text)) return true;
      if (/^[A-Z]{1,4}$/.test(text)) return true;
      return [
        'basicservices',
        'additionalservices',
        'date',
        'description',
        'code',
        'hrs',
        'hours',
        'extension',
        'total',
        'subtotal',
      ].includes(key);
    },

    extractLineItemQuantity(row, column, description) {
      if (!column) return '';
      const direct = String(row[column.index] || '').trim();
      const next = this.findNearbyLineItemNumber(row, column.index + 1, 2);
      if ((!direct || /total/i.test(description)) && next) return next;
      return direct || next;
    },

    findNearbyLineItemNumber(row, startIndex, maxDistance) {
      for (let offset = 0; offset <= maxDistance; offset += 1) {
        const value = String(row[startIndex + offset] || '').trim();
        if (this.isNumericCell(value) && !this.isMoneyCell(value) && !/%$/.test(value)) return value;
      }
      return '';
    },

    extractLineItemTotal(row, column) {
      const direct = this.extractLineItemDirect(row, column);
      if (direct) return direct;
      const moneyCells = row.map(cell => String(cell || '').trim()).filter(cell => this.isMoneyCell(cell));
      return moneyCells[moneyCells.length - 1] || '';
    },

    currentLineItemHeaders() {
      return this.lineItemData().headers;
    },

    currentLineItemRows() {
      return this.lineItemData().rows;
    },

    normalizedLineItemHeader(header) {
      return String(header || '').toLowerCase().replace(/[^a-z0-9]+/g, '');
    },

    isSummaryLineItemRow(row) {
      const cells = row.map(cell => String(cell || '').trim());
      const normalizedCells = cells.map(cell => cell.toLowerCase().replace(/[^a-z0-9]+/g, ''));
      const joined = normalizedCells.join('');
      if (!joined) return true;
      if ([
        'remarksinstructions',
        'makecheckspayable',
        'subtotal',
        'taxrate',
        'salestax',
        'balancedue',
        'invoicetotal',
        'previousbalance',
        'paymentsreceived',
        'accountbalance',
      ].some(marker => joined.includes(marker))) {
        return true;
      }
      if ((joined.includes('total') || joined.includes('balance')) && !cells.some(cell => this.dateFromLineItemCell(cell))) {
        return true;
      }
      const labelCells = normalizedCells.filter((cell, index) => cell && !this.isNumericCell(cells[index]));
      return labelCells.length > 0 && labelCells.every(cell => cell === 'total');
    },

    isRepeatedSectionLabelRow(row) {
      const values = row.map(cell => this.normalizedLineItemHeader(cell)).filter(Boolean);
      if (values.length < 2) return false;
      const unique = Array.from(new Set(values));
      return unique.length === 1 && /services|summary|fees/.test(unique[0]);
    },

    lineItemColumnRole(header, index) {
      const key = this.normalizedLineItemHeader(header);
      if (key === 'date') return 'text';
      if (key.includes('description') || key.includes('service') || key.includes('work')) return 'description';
      if (key.includes('total') || key.includes('price') || key.includes('rate') || key.includes('amount') || key.includes('hour') || key === 'qty' || key.includes('quantity')) return 'number';
      if (key.includes('item') || index === 0) return 'item';
      return 'text';
    },

    lineItemColumnStyle(header, index) {
      const key = this.normalizedLineItemHeader(header);
      const role = this.lineItemColumnRole(header, index);
      if (role === 'description') return 'width: auto; min-width: 22rem;';
      if (key === 'date') return 'width: 7.5rem; min-width: 7.5rem;';
      if (key.includes('total')) return 'width: 8.5rem; min-width: 8.5rem;';
      if (key.includes('rate')) return 'width: 8rem; min-width: 8rem;';
      if (key.includes('hour') || key === 'qty' || key.includes('quantity')) return 'width: 5.5rem; min-width: 5.5rem;';
      if (role === 'item') return 'width: 4rem; min-width: 4rem;';
      return 'width: 8rem; min-width: 7rem;';
    },

    lineItemHeaderClass(header, index) {
      const role = this.lineItemColumnRole(header, index);
      return role === 'number'
        ? 'p-3 whitespace-nowrap text-right'
        : 'p-3 whitespace-nowrap text-left';
    },

    lineItemCellClass(header, cell, index) {
      const role = this.lineItemColumnRole(header, index);
      if (role === 'number' && String(cell || '').trim() === '--') {
        return 'p-3 align-middle text-right font-mono text-flowly-textMuted whitespace-nowrap';
      }
      if (role === 'number' && !this.isNumericCell(cell) && !this.isMoneyCell(cell)) {
        return 'p-3 align-middle text-left text-flowly-textMain leading-5 whitespace-normal break-words';
      }
      if (role === 'number' || this.isNumericCell(cell)) {
        return 'p-3 align-middle text-right font-mono font-semibold tabular-nums text-flowly-textMain whitespace-nowrap';
      }
      if (role === 'item') {
        return 'p-3 align-middle text-left font-mono font-semibold text-flowly-textMain whitespace-nowrap';
      }
      return 'p-3 align-middle text-left text-flowly-textMain leading-5 whitespace-normal break-words';
    },

    displayLineItemCell(header, cell, index) {
      return this.displayLineItemRoleValue(this.lineItemDisplayRole(header, index), cell);
    },

    lineItemDisplayRole(header, index) {
      const key = this.normalizedLineItemHeader(header);
      if (key.includes('description') || key.includes('service') || key.includes('work')) return 'description';
      if (key.includes('total')) return 'total';
      if (key.includes('rate') || key.includes('price')) return 'rate';
      if (key.includes('hour') || key === 'qty' || key.includes('quantity')) return 'quantity';
      return index === 0 ? 'description' : '';
    },

    displayLineItemRoleValue(role, value) {
      if (role === 'description') return this.cleanLineItemDescription(value);
      if (role === 'quantity') return this.normalizeLineItemQuantity(value);
      if (role === 'rate' || role === 'total') return this.normalizeLineItemMoney(value);
      return String(value || '').trim();
    },

    normalizeLineItemQuantity(value) {
      let text = String(value || '').trim();
      if (!text) return '--';
      text = text.replace(/\s*(h|hr|hrs|hours)\b\.?/i, '').trim();
      const compact = text.replace(/,/g, '');
      if (!/^-?\d+(?:\.\d+)?$/.test(compact)) return text;
      const numberValue = Number(compact);
      if (!Number.isFinite(numberValue)) return text;
      return Number.isInteger(numberValue)
        ? String(numberValue)
        : String(numberValue).replace(/(\.\d*?)0+$/, '$1').replace(/\.$/, '');
    },

    normalizeLineItemMoney(value) {
      const text = String(value || '').trim();
      if (!text || text === '--') return '--';

      const compact = text.replace(/,/g, '');
      const match = compact.match(/^([$\u20ac\u00a3])?\s*(-?\d+(?:\.\d+)?)\s*([$\u20ac\u00a3])?$/);
      if (!match) return text;

      const symbol = match[1] || match[3] || '$';
      const numberValue = Number(match[2]);
      if (!Number.isFinite(numberValue)) return text;
      return `${symbol}${numberValue.toLocaleString('en-US', {
        minimumFractionDigits: 2,
        maximumFractionDigits: 2,
      })}`;
    },

    cleanLineItemDescription(value) {
      let text = String(value || '').trim();
      if (!text) return '';

      const phraseFixes = [
        [/developmentof/gi, 'Development of'],
        [/researchon/gi, 'Research on'],
        [/inproject/gi, 'in project'],
        [/googledatastudiodashboard/gi, 'Google data studio dashboard'],
        [/googledatastudio/gi, 'Google data studio'],
        [/coverphotosforfacebookpage/gi, 'Cover photos for facebook page'],
        [/coverphotosfor/gi, 'Cover photos for'],
        [/facebookpage/gi, 'facebook page'],
        [/canvainstagrambanners/gi, 'Canva instagram banners'],
        [/instagrambanners/gi, 'instagram banners'],
        [/paypalintegration/gi, 'Paypal integration'],
        [/preconstructionmeeting/gi, 'Preconstruction Meeting'],
      ];

      phraseFixes.forEach(([pattern, replacement]) => {
        text = text.replace(pattern, replacement);
      });

      return text
        .replace(/\b\d{2}\s*contract\s*administration\s*total:?/gi, '')
        .replace(/\bcontract\s*administration\s*total:?/gi, '')
        .replace(/\b[a-z0-9 /-]+\s+sub\s*total:?$/gi, '')
        .replace(/\b[a-z0-9 /-]+\s+total:?$/gi, '')
        .replace(/([a-z])([A-Z])/g, '$1 $2')
        .replace(/([A-Za-z])(\d+D)/g, '$1 $2')
        .replace(/(\d+D)([A-Za-z])/g, '$1 $2')
        .replace(/\s+/g, ' ')
        .trim();
    },

    isNumericCell(value) {
      return /^[$\u20ac\u00a3]?\s*-?\d+(?:[,.]\d+)?\s*(?:h|hrs?)?$/i.test(String(value || '').trim());
    },

    isMoneyCell(value) {
      return /^[$\u20ac\u00a3]\s*-?\d/.test(String(value || '').trim());
    },

    isLineItemAmountCell(value) {
      const text = String(value || '').trim();
      if (this.isMoneyCell(text)) return true;
      if (!this.isNumericCell(text)) return false;
      if (/\d\s*(h|hr|hrs|hours)\b/i.test(text)) return false;
      return /[,.]\d{2}\s*$/.test(text);
    },

    async loadDaemonLogs() {
      try {
        this.daemonLogs = await window.go.main.App.GetDaemonLogs();
      } catch (err) {
        this.daemonLogs = "Failed to load daemon logs: " + err;
      }
    },

    // UI helpers
    triggerToast(message) {
      const toast = document.getElementById('toast');
      const toastMsg = document.getElementById('toast-message');
      if (toast && toastMsg) {
        toastMsg.innerText = message;
        toast.classList.remove('opacity-0', 'translate-y-[-20px]', 'pointer-events-none');
        toast.classList.add('toast-animate-in');
        setTimeout(() => {
          toast.classList.add('opacity-0', 'translate-y-[-20px]', 'pointer-events-none');
          toast.classList.remove('toast-animate-in');
        }, 4000);
      }
    },

    formatBytes(bytes) {
      if (bytes === 0) return '0 Bytes';
      const k = 1024;
      const sizes = ['Bytes', 'KB', 'MB', 'GB'];
      const i = Math.floor(Math.log(bytes) / Math.log(k));
      return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
    },

    getConfidenceBadgeClass(confidence) {
      if (confidence >= 0.90) return 'text-spruce-700 bg-spruce-50 border-spruce-100';
      if (confidence >= 0.75) return 'text-amber-700 bg-amber-50 border-amber-100';
      return 'text-red-700 bg-red-50 border-red-100';
    }
  }
}
