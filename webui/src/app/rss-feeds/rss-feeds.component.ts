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

interface RssSource {
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

const RSS_SOURCES_QUERY = gql`
  query RssSources {
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
  selector: "app-rss-feeds",
  standalone: true,
  imports: [AppModule, DocumentTitleComponent],
  templateUrl: "./rss-feeds.component.html",
  styleUrl: "./rss-feeds.component.scss",
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class RssFeedsComponent implements OnInit {
  private apollo = inject(Apollo);
  private http = inject(HttpClient);
  private errors = inject(ErrorsService);

  sources = signal<RssSource[]>([]);
  loading = signal(true);
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  error = signal<any>(null);
  polling = signal<Set<string>>(new Set());

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
        query: RSS_SOURCES_QUERY,
        fetchPolicy: "network-only",
      })
      .subscribe({
        next: (result) => {
          const all =
            result.data?.torrentContent?.search?.aggregations?.torrentSource ??
            [];
          this.sources.set(
            all
              .filter((s) => s.value.startsWith("rss-"))
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

  isPolling(sourceKey: string): boolean {
    return this.polling().has(sourceKey);
  }

  pollNow(sourceKey: string): void {
    // Extract feed name from key format "rss-<name>"
    const name = sourceKey.replace(/^rss-/, "");
    if (!name) return;

    this.polling.update((s) => new Set([...s, sourceKey]));

    this.http
      .post("/api/rss/poll", { feedNames: [name] })
      .subscribe({
        complete: () =>
          this.polling.update((s) => {
            const next = new Set(s);
            next.delete(sourceKey);
            return next;
          }),
        error: () => {
          this.errors.addError("RSS feed poll request failed");
          this.polling.update((s) => {
            const next = new Set(s);
            next.delete(sourceKey);
            return next;
          });
        },
      });
  }
}
