import {
  ChangeDetectionStrategy,
  Component,
  inject,
  OnInit,
  signal,
} from "@angular/core";
import { Apollo, gql } from "apollo-angular";
import { AppModule } from "../app.module";
import { DocumentTitleComponent } from "../layout/document-title.component";

interface ProwlarrSource {
  key: string;
  name: string;
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

  sources = signal<ProwlarrSource[]>([]);
  loading = signal(true);
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  error = signal<any>(null);

  ngOnInit() {
    this.apollo
      .query<{
        torrentContent: {
          search: {
            aggregations: {
              torrentSource: Array<{
                value: string;
                label: string;
                count: number;
                isEstimate: boolean;
              }>;
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
            result.data?.torrentContent?.search?.aggregations
              ?.torrentSource ?? [];
          this.sources.set(
            all.filter((s) => s.value.startsWith("prowlarr-")),
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
}
