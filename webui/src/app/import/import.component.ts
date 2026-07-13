import {
  ChangeDetectionStrategy,
  Component,
  inject,
  signal,
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
export class ImportComponent {
  private http = inject(HttpClient);

  // State
  phase = signal<"upload" | "analyze" | "configure" | "importing" | "done">(
    "upload",
  );
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

  // Import results
  importResult = signal<{
    source: string;
    imported: number;
    skipped: number;
  } | null>(null);

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

    // If all are selected, send empty array (= import all).
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
      .post<{
        source: string;
        imported: number;
        skipped: number;
      }>("/api/import/execute", formData)
      .subscribe({
        next: (result) => {
          this.importResult.set(result);
          this.importing.set(false);
          this.phase.set("done");
        },
        error: (err) => {
          this.error.set(
            err.error?.error || "Import failed",
          );
          this.importing.set(false);
          this.phase.set("configure");
        },
      });
  }

  reset(): void {
    this.phase.set("upload");
    this.analysis.set(null);
    this.categories.set([]);
    this.sourceName.set("");
    this.selectedFile.set(null);
    this.importResult.set(null);
    this.error.set(null);
  }
}
