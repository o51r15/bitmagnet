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

interface FeedStatus {
  name: string;
  sourceKey: string;
  url: string;
  enabled: boolean;
  interval: string;
  lastPolled?: string;
}

interface RssFeed {
  name: string;
  sourceKey: string;
  url: string;
  enabled: boolean;
  interval: string;
  lastPolled?: string;
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

  feeds = signal<RssFeed[]>([]);
  loading = signal(true);
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  error = signal<any>(null);
  polling = signal<Set<string>>(new Set());

  ngOnInit() {
    this.load();
  }

  private load() {
    this.loading.set(true);
    // Fetch configured feeds and torrent-source counts in parallel.
    this.http.get<{ feeds: FeedStatus[] }>("/api/rss/feeds").subscribe({
      next: (res) => {
        const configured = res.feeds ?? [];
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
              const aggs =
                result.data?.torrentContent?.search?.aggregations
                  ?.torrentSource ?? [];
              const countByKey = new Map(
                aggs
                  .filter((s) => s.value.startsWith("rss-"))
                  .map((s) => [
                    s.value,
                    { count: s.count, isEstimate: s.isEstimate },
                  ]),
              );
              this.feeds.set(
                configured.map((f) => {
                  const agg = countByKey.get(f.sourceKey);
                  return {
                    ...f,
                    count: agg?.count ?? 0,
                    isEstimate: agg?.isEstimate ?? false,
                  };
                }),
              );
              this.loading.set(false);
            },
            error: () => {
              // GraphQL failed — still show configured feeds with zero counts.
              this.feeds.set(
                configured.map((f) => ({ ...f, count: 0, isEstimate: false })),
              );
              this.loading.set(false);
            },
          });
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

  isPolling(feedName: string): boolean {
    return this.polling().has(feedName);
  }

  pollNow(feedName: string): void {
    if (!feedName) return;

    this.polling.update((s) => new Set([...s, feedName]));

    this.http.post("/api/rss/poll", { feedNames: [feedName] }).subscribe({
      complete: () =>
        this.polling.update((s) => {
          const next = new Set(s);
          next.delete(feedName);
          return next;
        }),
      error: () => {
        this.errors.addError("RSS feed poll request failed");
        this.polling.update((s) => {
          const next = new Set(s);
          next.delete(feedName);
          return next;
        });
      },
    });
  }
}
