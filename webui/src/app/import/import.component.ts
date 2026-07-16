import {
  ChangeDetectionStrategy,
  Component,
  inject,
  signal,
  OnDestroy,
} from "@angular/core";
import { HttpClient } from "@angular/common/http";
import { AppModule } from "../app.module";
import { DocumentTitleComponent } from "../layout/document-title.component";

interface AnalysisResult {
  format: string;
  totalRows: number;
  categories: Record<string, number>;
  errors: number;
}

interface ImportJob {
  id: string;
  source: string;
  sourceName: string;
  phase: string;
  total: number;
  imported: number;
  skipped: number;
  error?: string;
  startedAt: string;
  updatedAt: string;
}

interface StatusResponse {
  active: boolean;
  job?: ImportJob;
}

interface CategoryOption {
  key: string;
  count: number;
  selected: boolean;
}

@Component({
  selector: "app-import",
  standalone: true,
  imports: [AppModule, DocumentTitleComponent],
  templateUrl: "./import.component.html",
  styleUrl: "./import.component.scss",
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ImportComponent implements OnDestroy {
  private http = inject(HttpClient);

  // State
  phase = signal<
    "upload" | "analyze" | "configure" | "importing" | "done"
  >("upload");
  analyzing = signal(false);
  importing = signal(false);
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  error = signal<any>(null);

  // Analysis results
  analysis = signal<AnalysisResult | null>(null);
  categories = signal<CategoryOption[]>([]);

  // User inputs
  sourceName = signal("");
  selectedFile = signal<File | null>(null);

  // Import job tracking
  currentJob = signal<ImportJob | null>(null);

  // Import results (for done phase)
  importResult = signal<{
    source: string;
    imported: number;
    skipped: number;
  } | null>(null);

  // Polling
  private pollTimer: ReturnType<typeof setInterval> | null = null;

  constructor() {
    // Check if there's already an import running on load.
    this.checkExistingJob();
  }

  ngOnDestroy(): void {
    this.stopPolling();
  }

  private checkExistingJob(): void {
    this.http.get<StatusResponse>("/api/import/status").subscribe({
      next: (resp) => {
        if (resp.active && resp.job) {
          this.currentJob.set(resp.job);
          this.phase.set("importing");
          this.importing.set(true);
          this.startPolling();
        }
      },
    });
  }

  onFileSelected(event: Event): void {
    const input = event.target as HTMLInputElement;
    if (input.files && input.files.length > 0) {
      this.selectedFile.set(input.files[0]);
      this.error.set(null);
    }
  }

  analyzeFile(): void {
    const file = this.selectedFile();
    if (!file) return;

    this.analyzing.set(true);
    this.error.set(null);
    this.phase.set("analyze");

    const formData = new FormData();
    formData.append("file", file);

    this.http
      .post<AnalysisResult>("/api/import/analyze", formData)
      .subscribe({
        next: (result) => {
          this.analysis.set(result);
          const cats = Object.entries(result.categories).map(
            ([key, count]) => ({
              key,
              count,
              selected: true,
            }),
          );
          cats.sort((a, b) => b.count - a.count);
          this.categories.set(cats);
          this.analyzing.set(false);
          this.phase.set("configure");
        },
        error: (err) => {
          this.error.set(
            err.error?.error || "Failed to analyze file",
          );
          this.analyzing.set(false);
          this.phase.set("upload");
        },
      });
  }

  toggleCategory(key: string): void {
    this.categories.update((cats) =>
      cats.map((c) =>
        c.key === key ? { ...c, selected: !c.selected } : c,
      ),
    );
  }

  selectAll(): void {
    this.categories.update((cats) =>
      cats.map((c) => ({ ...c, selected: true })),
    );
  }

  deselectAll(): void {
    this.categories.update((cats) =>
      cats.map((c) => ({ ...c, selected: false })),
    );
  }

  executeImport(): void {
    const file = this.selectedFile();
    const name = this.sourceName().trim();
    if (!file || !name) return;

    const selectedCats = this.categories()
      .filter((c) => c.selected)
      .map((c) => c.key);

    const allSelected =
      selectedCats.length === this.categories().length;

    this.importing.set(true);
    this.error.set(null);
    this.phase.set("importing");

    const formData = new FormData();
    formData.append("file", file);
    formData.append(
      "config",
      JSON.stringify({
        sourceName: name,
        categories: allSelected ? [] : selectedCats,
      }),
    );

    this.http
      .post<{ status: string; job: ImportJob }>(
        "/api/import/execute",
        formData,
      )
      .subscribe({
        next: (result) => {
          this.currentJob.set(result.job);
          this.startPolling();
        },
        error: (err) => {
          const errorMsg =
            err.error?.error || "Failed to start import";
          this.error.set(errorMsg);
          this.importing.set(false);
          this.phase.set("configure");
        },
      });
  }

  private startPolling(): void {
    this.stopPolling();
    this.pollTimer = setInterval(() => this.pollStatus(), 2000);
  }

  private stopPolling(): void {
    if (this.pollTimer) {
      clearInterval(this.pollTimer);
      this.pollTimer = null;
    }
  }

  private pollStatus(): void {
    this.http.get<StatusResponse>("/api/import/status").subscribe({
      next: (resp) => {
        if (!resp.job) {
          this.stopPolling();
          return;
        }

        this.currentJob.set(resp.job);

        if (resp.job.phase === "complete") {
          this.stopPolling();
          this.importing.set(false);
          this.importResult.set({
            source: resp.job.source,
            imported: resp.job.imported,
            skipped: resp.job.skipped,
          });
          this.phase.set("done");
        } else if (resp.job.phase === "failed") {
          this.stopPolling();
          this.importing.set(false);
          this.error.set(resp.job.error || "Import failed");
          this.phase.set("configure");
          this.currentJob.set(null);
        }
      },
      error: () => {
        // Network error — keep polling, it might recover.
      },
    });
  }

  /** Format a number with commas. */
  formatNumber(n: number): string {
    return n.toLocaleString();
  }

  /** Elapsed time since job started. */
  getElapsed(): string {
    const job = this.currentJob();
    if (!job) return "";
    const start = new Date(job.startedAt).getTime();
    const now = Date.now();
    const secs = Math.floor((now - start) / 1000);
    if (secs < 60) return `${secs}s`;
    const mins = Math.floor(secs / 60);
    const remSecs = secs % 60;
    return `${mins}m ${remSecs}s`;
  }

  reset(): void {
    this.phase.set("upload");
    this.analysis.set(null);
    this.categories.set([]);
    this.sourceName.set("");
    this.selectedFile.set(null);
    this.importResult.set(null);
    this.currentJob.set(null);
    this.error.set(null);
    this.stopPolling();
  }
}
