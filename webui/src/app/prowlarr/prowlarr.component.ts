import {
  ChangeDetectionStrategy,
  Component,
  inject,
  OnInit,
  signal,
} from "@angular/core";
import { HttpClient } from "@angular/common/http";
import { Apollo, gql } from "apollo-angular";
import { AppModule } from "../app.module";
import { DocumentTitleComponent } from "../layout/document-title.component";
import { ErrorsService } from "../errors/errors.service";

interface ProwlarrSource {
  key: string;
  name: string;
  count: number;
  isEstimate: boolean;
}

interface TorrentSourceAgg {
  value: string;
  label: string;
  count: number;
  isEstimate: boolean;
}

const PROWLARR_SOURCES_QUERY = gql`
  query ProwlarrSources {
    torrentContent {
      search(
        input: { limit: 1, facets: { torrentSource: { aggregate: true } } }
      ) {
        aggregations {
          torrentSource {
            value
            label
            count
            isEstimate
          }
        }
      }
    }
  }
`;

@Component({
  selector: "app-prowlarr",
  standalone: true,
  imports: [AppModule, DocumentTitleComponent],
  templateUrl: "./prowlarr.component.html",
  styleUrl: "./prowlarr.component.scss",
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ProwlarrComponent implements OnInit {
  private apollo = inject(Apollo);
  private http = inject(HttpClient);
  private errors = inject(ErrorsService);

  sources = signal<ProwlarrSource[]>([]);
  loading = signal(true);
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  error = signal<any>(null);
  // Set of source keys currently being crawled (used to disable the button).
  crawling = signal<Set<string>>(new Set());

  ngOnInit() {
    this.apollo
      .query<{
        torrentContent: {
          search: {
            aggregations: {
              torrentSource?: TorrentSourceAgg[];
            };
          };
        };
      }>({
        query: PROWLARR_SOURCES_QUERY,
        fetchPolicy: "network-only",
      })
      .subscribe({
        next: (result) => {
          const all =
            result.data?.torrentContent?.search?.aggregations?.torrentSource ??
            [];
          this.sources.set(
            all
              .filter((s) => s.value.startsWith("prowlarr-"))
              .map((s) => ({
                key: s.value,
                name: s.label,
                count: s.count,
                isEstimate: s.isEstimate,
              })),
          );
          this.loading.set(false);
        },
        error: (err) => {
          this.error.set(err);
          this.loading.set(false);
        },
      });
  }

  getSearchParams(sourceKey: string): Record<string, string> {
    return {
      facets: "torrent_source",
      torrent_source: sourceKey,
    };
  }

  isCrawling(sourceKey: string): boolean {
    return this.crawling().has(sourceKey);
  }

  crawlNow(sourceKey: string): void {
    // Extract numeric ID from key format "prowlarr-<id>"
    const parts = sourceKey.split("-");
    const id = parseInt(parts[1], 10);
    if (isNaN(id)) return;

    // Mark as in-flight
    this.crawling.update((s) => new Set([...s, sourceKey]));

    this.http
      .post("/api/prowlarr/crawl", { indexerIds: [id] })
      .subscribe({
        complete: () =>
          this.crawling.update((s) => {
            const next = new Set(s);
            next.delete(sourceKey);
            return next;
          }),
        error: () => {
          this.errors.addError("Prowlarr crawl request failed");
          this.crawling.update((s) => {
            const next = new Set(s);
            next.delete(sourceKey);
            return next;
          });
        },
      });
  }
}
