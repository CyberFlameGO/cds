import {
    ChangeDetectionStrategy,
    Component,
    EventEmitter,
    Input,
    OnChanges,
    Output,
    Pipe,
    PipeTransform
} from '@angular/core';
import { Table } from './table';

type direction = string;
const ASC: direction = 'asc';
const DESC: direction = 'desc';

export enum ColumnType {
    TEXT = 'text',
    TEXT_HTML = 'text-html',
    ICON = 'icon',
    IMG_TEXT = 'img-text',
    LINK_CLICK = 'link-click',
    LINK = 'link',
    ROUTER_LINK = 'router-link',
    ROUTER_LINK_WITH_ICONS = 'router-link-with-icons',
    ROUTER_LINK_WITH_LABELS = 'router-link-with-labels',
    MARKDOWN = 'markdown',
    DATE = 'date',
    BUTTON = 'button',
    CONFIRM_BUTTON = 'confirm-button',
    LABEL = 'label',
    TEXT_COPY = 'text-copy',
    TEXT_LABELS = 'text-labels',
    TEXT_ICONS = 'text-icons',
    TIME_AGO = 'time-ago'
}

export type SelectorType<T> = (d: T) => ColumnType;
export type SelectorFlag<T> = (d: T) => boolean;
export type Selector<T> = (d: T, index?: number) => any;
export type Filter<T> = (f: string) => (d: T) => boolean;
export type Select<T> = (d: T) => boolean;

export class Column<T> {
    type: ColumnType | SelectorType<T>;
    name: string;
    class: string;
    selector: Selector<T>;
    sortable: boolean;
    sortKey: string;
    disabled: boolean;
    hidden: SelectorFlag<T>;
}

@Pipe({ name: 'selector' })
export class SelectorPipe<T> implements PipeTransform {
    transform(columns: Array<Column<T>>, data: T, index?: number): Array<any> {
        return columns.map(c => {
            let type: ColumnType;
            switch (typeof c.type) {
                case 'function':
                    type = c.type(data);
                    break;
                default:
                    type = c.type;
                    break;
            }

            let selector = c.selector(data, index);

            let translate: boolean;
            if (!type || type === ColumnType.TEXT) {
                translate = typeof selector === 'string';
            }

            return {
                ...c,
                type,
                selector,
                translate,
                hidden: c.hidden && c.hidden(data)
            };
        });
    }
}

@Pipe({ name: 'select' })
export class SelectPipe<T extends WithKey> implements PipeTransform {
    transform(selected: Array<string>, data: T): boolean {
        return !!selected.find(s => s === data.key());
    }
}

export interface WithKey {
    key(): string;
}

@Component({
    selector: 'app-data-table',
    templateUrl: './data-table.html',
    styleUrls: ['./data-table.scss'],
    changeDetection: ChangeDetectionStrategy.OnPush
})
export class DataTableComponent<T extends WithKey> extends Table<T> implements OnChanges {
    @Input() columns: Array<Column<T>>;
    @Output() sortChange = new EventEmitter<string>();
    @Output() dataChange = new EventEmitter<number>();
    @Output() filterChange = new EventEmitter<string>();
    @Input() loading: boolean;
    @Input() withLineClick: boolean;
    @Output() clickLine = new EventEmitter<T>();
    @Output() selectChange = new EventEmitter<Array<string>>();
    @Input() withSelect: boolean | Select<T>;
    @Input() activeLine: Select<T>;
    selectedAll: boolean;
    selected: Object = {};
    @Input() data: Array<T>;
    @Input() withPagination: number;
    @Input() withFilter: Filter<T>;
    sortedColumn: Column<T>;
    sortedColumnDirection: direction;
    allData: Array<T>;
    dataForCurrentPage: any;
    pagesCount: number;
    filterFunc: Filter<T>;
    @Input() filter: string;
    filteredData: Array<T>;
    indexSelected: number;
    columnsCount: number;

    constructor() {
        super();
    }

    static filterArgsFunc(rawSearch: string, matchFunc: (search: string, entry: any) => boolean): (d: any) => boolean {
        const rawFilter = rawSearch.toLowerCase().split(' ');
        let filters = {};
        let words = [];
        rawFilter.forEach(w => {
            const filter = w.split(':');
            if (filter.length === 2) {
                filters[filter[0]] = filter[1];
            } else {
                words.push(w);
            }
        });
        const lowerFilter = words.join(' ');
        return (e: any) => {
            let matchAllFilter = null;
            if (Object.keys(filters).length > 0) {
                matchAllFilter = Object.keys(filters).map(k => !!e[k] && e[k].toLowerCase() === filters[k]).reduce((p, c) => p && c);
            }
            return (matchAllFilter === null || matchAllFilter) && matchFunc(lowerFilter, e);
        };
    }

    ngOnChanges() {
        this.allData = this.data;

        if (this.allData) {
            if (this.withSelect) {
                this.allData.forEach(d => this.selected[d.key()] = false);
                if (typeof this.withSelect === 'function') {
                    this.allData.filter(this.withSelect).forEach(d => this.selected[d.key()] = true);
                    this.emitSelectChange();
                }
            }

            if (this.activeLine) {
                this.allData.forEach((data, index) => {
                    if (this.activeLine(data)) {
                        this.indexSelected = index;
                    }
                });
            }
        }

        this.nbElementsByPage = this.withPagination;
        this.filterFunc = this.withFilter;
        this.columnsCount = this.columns.filter(c => !c.disabled).length + (this.withSelect ? 1 : 0);
        this.getDataForCurrentPage();
    }

    columnClick(event: Event, c: Column<T>) {
        if (!c.sortable) {
            return;
        }

        this.sortedColumn = c;
        if (!this.sortedColumnDirection) {
            this.sortedColumnDirection = ASC;
        } else {
            this.sortedColumnDirection = this.sortedColumnDirection === ASC ? DESC : ASC;
        }

        this.sortChange.emit(this.sortedColumn.sortKey + ':' + this.sortedColumnDirection);
    }

    getData(): Array<T> {
        this.filteredData = this.data;
        if (this.filteredData) {
            if (this.filter && this.filterFunc) {
                this.filteredData = this.data.filter(this.filterFunc(this.filter));
            }
            this.dataChange.emit(this.filteredData.length);
        }

        return this.filteredData;
    }

    getDataForCurrentPage(): Array<T> {
        this.pagesCount = this.getNbOfPages();
        if (this.pagesCount < this.currentPage) {
            this.currentPage = 1;
        }

        let data: Array<T>;
        if (!this.withPagination) {
            data = this.getData();
        } else {
            data = super.getDataForCurrentPage();
        }
        this.dataForCurrentPage = data;

        return data;
    }

    filterKeyUp() {
        this.getDataForCurrentPage();
        this.filterChange.emit(this.filter);
    }

    pageChange(n: number) {
        this.goTopage(n);
    }

    lineClick(i: number, d: T) {
        if (this.withLineClick) {
            this.indexSelected = i;
            this.clickLine.emit(d);
        }
    }

    onSelectAllChange(e: any) {
        this.selectedAll = !this.selectedAll;
        this.allData.forEach(d => this.selected[d.key()] = this.selectedAll);
        this.emitSelectChange();
    }

    onSelectChange(e: any, key: string) {
        this.selected[key] = e;
        this.emitSelectChange();
    }

    emitSelectChange() {
        this.selectChange.emit(Object.keys(this.selected).filter(k => this.selected[k]));
    }
}
