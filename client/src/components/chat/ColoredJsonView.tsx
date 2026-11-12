import { isDateString } from '../../utils/queryUtils';
import { zhCN } from '../../i18n/zh-CN';

interface ColoredJsonViewProps {
    data: unknown;
    nonTechMode?: boolean;
    indent?: number;
}

/**
 * Recursive syntax-highlighted JSON renderer.
 * Renders strings, numbers, booleans, null, arrays and objects with colour classes.
 */
export default function ColoredJsonView({ data, nonTechMode, indent = 0 }: ColoredJsonViewProps) {
    const indentStr = '  '.repeat(indent);

    if (data === null) {
        return nonTechMode
            ? <span className="text-gray-400">{zhCN.query.noData}</span>
            : <span className="text-yellow-400">null</span>;
    }

    if (data === undefined) return <span className="text-yellow-400">undefined</span>;

    if (typeof data === 'boolean') return <span className="text-purple-400">{String(data)}</span>;

    if (typeof data === 'number') return <span className="text-cyan-400">{data}</span>;

    if (typeof data === 'string') {
        if (isDateString(data)) return <span className="text-yellow-300">"{data}"</span>;
        return <span className="text-green-400">"{data}"</span>;
    }

    if (Array.isArray(data)) {
        if (data.length === 0) return <span>[]</span>;
        return (
            <span>
                <span>[</span>
                <div style={{ marginLeft: 20 }}>
                    {data.map((item, index) => (
                        <div key={index}>
                            <ColoredJsonView data={item} nonTechMode={nonTechMode} indent={indent + 1} />
                            {index < data.length - 1 && <span>,</span>}
                        </div>
                    ))}
                </div>
                <span>{indentStr}]</span>
            </span>
        );
    }

    if (typeof data === 'object') {
        const entries = Object.entries(data);
        if (entries.length === 0) return <span>{'{}'}</span>;
        return (
            <span>
                <span>{'{'}</span>
                <div style={{ marginLeft: 20 }}>
                    {entries.map(([key, value], index) => (
                        <div key={key}>
                            <span className="text-blue-400">"{key}"</span>
                            <span>: </span>
                            <ColoredJsonView data={value} nonTechMode={nonTechMode} indent={indent + 1} />
                            {index < entries.length - 1 && <span>,</span>}
                        </div>
                    ))}
                </div>
                <span>{indentStr}{'}'}</span>
            </span>
        );
    }

    return <span>{String(data)}</span>;
}
